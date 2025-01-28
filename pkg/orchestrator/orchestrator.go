package orchestrator

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

const (
	ComponentName      = "orc"
	componentShortName = "orc"
	defaultWebPort     = 3000
	defaultRaftPort    = 10008
	configVolumeName   = "config"
	configMountPath    = "/etc/orchestrator/config"
	ConfigFileName     = "orchestrator.conf.json"
	credsVolumeName    = "users"
	CredsMountPath     = "/etc/orchestrator/orchestrator-users-secret"
	tlsVolumeName      = "tls"
	tlsMountPath       = "/etc/orchestrator/ssl"
)

type Exposer apiv1alpha1.PerconaServerMySQL

func (e *Exposer) Exposed() bool {
	cr := apiv1alpha1.PerconaServerMySQL(*e)
	return cr.OrchestratorEnabled()
}

func (e *Exposer) Name(index string) string {
	cr := apiv1alpha1.PerconaServerMySQL(*e)
	return Name(&cr) + "-" + index
}

func (e *Exposer) Size() int32 {
	return e.Spec.Orchestrator.Size
}

func (e *Exposer) Labels() map[string]string {
	cr := apiv1alpha1.PerconaServerMySQL(*e)
	return MatchLabels(&cr)
}

func (e *Exposer) Service(name string) *corev1.Service {
	cr := apiv1alpha1.PerconaServerMySQL(*e)
	return PodService(&cr, cr.Spec.Orchestrator.Expose.Type, name)
}

func (e *Exposer) SaveOldMeta() bool {
	cr := apiv1alpha1.PerconaServerMySQL(*e)
	return cr.OrchestratorSpec().Expose.SaveOldMeta()
}

// Name returns component name
func Name(cr *apiv1alpha1.PerconaServerMySQL) string {
	return cr.Name + "-" + componentShortName
}

func NamespacedName(cr *apiv1alpha1.PerconaServerMySQL) types.NamespacedName {
	return types.NamespacedName{Name: Name(cr), Namespace: cr.Namespace}
}

func ServiceName(cr *apiv1alpha1.PerconaServerMySQL) string {
	return Name(cr)
}

func ConfigMapName(cr *apiv1alpha1.PerconaServerMySQL) string {
	return Name(cr)
}

func PodName(cr *apiv1alpha1.PerconaServerMySQL, idx int) string {
	return fmt.Sprintf("%s-%d", Name(cr), idx)
}

func FQDN(cr *apiv1alpha1.PerconaServerMySQL, idx int) string {
	// TODO: DNS suffix
	return fmt.Sprintf("%s.%s.svc", PodName(cr, idx), cr.Namespace)
}

func APIHost(cr *apiv1alpha1.PerconaServerMySQL) string {
	return fmt.Sprintf("http://%s:%d", FQDN(cr, 0), defaultWebPort)
}

// Labels returns labels of orchestrator
func Labels(cr *apiv1alpha1.PerconaServerMySQL) map[string]string {
	return cr.OrchestratorSpec().Labels
}

func MatchLabels(cr *apiv1alpha1.PerconaServerMySQL) map[string]string {
	return util.SSMapMerge(Labels(cr),
		map[string]string{naming.LabelComponent: ComponentName},
		cr.Labels())
}

func StatefulSet(cr *apiv1alpha1.PerconaServerMySQL, initImage, tlsHash string) *appsv1.StatefulSet {
	labels := MatchLabels(cr)
	spec := cr.OrchestratorSpec()
	Replicas := spec.Size

	annotations := make(map[string]string, 0)
	if tlsHash != "" {
		annotations[string(naming.AnnotationTLSHash)] = tlsHash
	}

	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      Name(cr),
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &Replicas,
			ServiceName: Name(cr),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			UpdateStrategy: updateStrategy(cr),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						k8s.InitContainer(
							ComponentName,
							initImage,
							spec.ImagePullPolicy,
							spec.ContainerSecurityContext,
							spec.Resources,
							nil,
						),
					},
					NodeSelector:                  cr.Spec.Orchestrator.NodeSelector,
					Tolerations:                   cr.Spec.Orchestrator.Tolerations,
					Containers:                    containers(cr),
					Affinity:                      spec.GetAffinity(labels),
					TopologySpreadConstraints:     spec.GetTopologySpreadConstraints(labels),
					ImagePullSecrets:              spec.ImagePullSecrets,
					TerminationGracePeriodSeconds: spec.TerminationGracePeriodSeconds,
					PriorityClassName:             spec.PriorityClassName,
					RuntimeClassName:              spec.RuntimeClassName,
					ServiceAccountName:            spec.ServiceAccountName,
					RestartPolicy:                 corev1.RestartPolicyAlways,
					SchedulerName:                 spec.SchedulerName,
					DNSPolicy:                     corev1.DNSClusterFirst,
					Volumes: []corev1.Volume{
						{
							Name: apiv1alpha1.BinVolumeName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: credsVolumeName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: cr.InternalSecretName(),
								},
							},
						},
						{
							Name: tlsVolumeName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: cr.Spec.SSLSecretName,
								},
							},
						},
						{
							Name: configVolumeName,
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: ConfigMapName(cr),
									},
								},
							},
						},
					},
					SecurityContext: spec.PodSecurityContext,
				},
			},
		},
	}
}

func updateStrategy(cr *apiv1alpha1.PerconaServerMySQL) appsv1.StatefulSetUpdateStrategy {
	switch cr.Spec.UpdateStrategy {
	case appsv1.OnDeleteStatefulSetStrategyType:
		return appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	default:
		var zero int32 = 0
		return appsv1.StatefulSetUpdateStrategy{
			Type: appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
				Partition: &zero,
			},
		}
	}
}

func containers(cr *apiv1alpha1.PerconaServerMySQL) []corev1.Container {
	sidecars := sidecarContainers(cr)
	containers := make([]corev1.Container, 1, len(sidecars)+1)
	containers[0] = container(cr)
	return append(containers, sidecars...)
}

func container(cr *apiv1alpha1.PerconaServerMySQL) corev1.Container {
	env := []corev1.EnvVar{
		{
			Name:  "ORC_SERVICE",
			Value: ServiceName(cr),
		},
		{
			Name:  "MYSQL_SERVICE",
			Value: mysql.ServiceName(cr),
		},
		{
			Name:  "RAFT_ENABLED",
			Value: "true",
		},
		{
			Name:  "CLUSTER_NAME",
			Value: cr.Name,
		},
	}
	env = append(env, cr.Spec.Orchestrator.Env...)

	return corev1.Container{
		Name:            ComponentName,
		Image:           cr.Spec.Orchestrator.Image,
		ImagePullPolicy: cr.Spec.Orchestrator.ImagePullPolicy,
		Resources:       cr.Spec.Orchestrator.Resources,
		Command:         []string{"/opt/percona/orc-entrypoint.sh"},
		Args:            []string{"/usr/local/orchestrator/orchestrator", "-config", "/etc/orchestrator/orchestrator.conf.json", "http"},
		Env:             env,
		EnvFrom:         cr.Spec.Orchestrator.EnvFrom,
		Ports: []corev1.ContainerPort{
			{
				Name:          "web",
				ContainerPort: defaultWebPort,
			},
			{
				Name:          "raft",
				ContainerPort: defaultRaftPort,
			},
		},
		VolumeMounts:             containerMounts(),
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          cr.Spec.Orchestrator.ContainerSecurityContext,
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/api/lb-check",
					Port: intstr.FromString("web"),
				},
			},
			InitialDelaySeconds: 10,
			TimeoutSeconds:      3,
			PeriodSeconds:       5,
			FailureThreshold:    3,
			SuccessThreshold:    1,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/api/health",
					Port: intstr.FromString("web"),
				},
			},
			InitialDelaySeconds: 30,
			TimeoutSeconds:      3,
			PeriodSeconds:       5,
			FailureThreshold:    3,
			SuccessThreshold:    1,
		},
	}
}

func sidecarContainers(cr *apiv1alpha1.PerconaServerMySQL) []corev1.Container {
	serviceName := mysql.ServiceName(cr)

	return []corev1.Container{
		{
			Name:            "mysql-monit",
			Image:           cr.Spec.Orchestrator.Image,
			ImagePullPolicy: cr.Spec.Orchestrator.ImagePullPolicy,
			Env: []corev1.EnvVar{
				{
					Name:  "ORC_SERVICE",
					Value: serviceName,
				},
				{
					Name:  "MYSQL_SERVICE",
					Value: serviceName,
				},
			},
			VolumeMounts: containerMounts(),
			Command:      []string{"/opt/percona/orc-entrypoint.sh"},
			Args: []string{
				"/opt/percona/peer-list",
				"-on-change=/usr/bin/add_mysql_nodes.sh",
				"-service=$(MYSQL_SERVICE)",
			},
			TerminationMessagePath:   "/dev/termination-log",
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
			SecurityContext:          cr.Spec.Orchestrator.ContainerSecurityContext,
			Resources:                cr.Spec.Orchestrator.Resources,
		},
	}
}

func containerMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      apiv1alpha1.BinVolumeName,
			MountPath: apiv1alpha1.BinVolumePath,
		},
		{
			Name:      tlsVolumeName,
			MountPath: tlsMountPath,
		},
		{
			Name:      configVolumeName,
			MountPath: configMountPath,
		},
		{
			Name:      credsVolumeName,
			MountPath: filepath.Join(CredsMountPath, string(apiv1alpha1.UserOrchestrator)),
			SubPath:   string(apiv1alpha1.UserOrchestrator),
		},
	}
}

func Service(cr *apiv1alpha1.PerconaServerMySQL) *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceName(cr),
			Namespace: cr.Namespace,
			Labels:    MatchLabels(cr),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Ports: []corev1.ServicePort{
				{
					Name: "web",
					Port: defaultWebPort,
				},
				{
					Name: "raft",
					Port: defaultRaftPort,
				},
			},
			Selector:                 MatchLabels(cr),
			PublishNotReadyAddresses: true,
		},
	}
}

func PodService(cr *apiv1alpha1.PerconaServerMySQL, t corev1.ServiceType, podName string) *corev1.Service {
	expose := cr.Spec.Orchestrator.Expose

	labels := MatchLabels(cr)
	labels[naming.LabelExposed] = "true"
	labels = util.SSMapMerge(expose.Labels, labels)

	selector := MatchLabels(cr)
	selector["statefulset.kubernetes.io/pod-name"] = podName
	selector = util.SSMapMerge(expose.Labels, selector)

	var loadBalancerSourceRanges []string
	if t == corev1.ServiceTypeLoadBalancer {
		loadBalancerSourceRanges = expose.LoadBalancerSourceRanges
	}

	var externalTrafficPolicy corev1.ServiceExternalTrafficPolicyType
	if t == corev1.ServiceTypeLoadBalancer || t == corev1.ServiceTypeNodePort {
		externalTrafficPolicy = expose.ExternalTrafficPolicy
	}

	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        podName,
			Namespace:   cr.Namespace,
			Labels:      labels,
			Annotations: expose.Annotations,
		},
		Spec: corev1.ServiceSpec{
			Type:     t,
			Selector: selector,
			Ports: []corev1.ServicePort{
				{
					Name: "web",
					Port: defaultWebPort,
				},
				{
					Name: "raft",
					Port: defaultRaftPort,
				},
			},
			LoadBalancerSourceRanges: loadBalancerSourceRanges,
			InternalTrafficPolicy:    expose.InternalTrafficPolicy,
			ExternalTrafficPolicy:    externalTrafficPolicy,
		},
	}
}

func ConfigMap(cr *apiv1alpha1.PerconaServerMySQL, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName(cr),
			Namespace: cr.Namespace,
		},
		Data: data,
	}
}

func RaftNodes(cr *apiv1alpha1.PerconaServerMySQL) []string {
	nodes := make([]string, cr.Spec.Orchestrator.Size)

	for i := 0; i < int(cr.Spec.Orchestrator.Size); i++ {
		nodes[i] = fmt.Sprintf("%s:%d", FQDN(cr, i), 10008)
	}

	return nodes
}

func orcConfig(cr *apiv1alpha1.PerconaServerMySQL) (string, error) {
	config := make(map[string]interface{}, 0)

	config["RaftNodes"] = RaftNodes(cr)
	configJson, err := json.Marshal(config)
	if err != nil {
		return "", errors.Wrap(err, "marshal orchestrator raft nodes to json")
	}

	return string(configJson), nil
}

func ConfigMapData(cr *apiv1alpha1.PerconaServerMySQL) (map[string]string, error) {
	cmData := make(map[string]string, 0)

	config, err := orcConfig(cr)
	if err != nil {
		return cmData, errors.Wrap(err, "get raft nodes")
	}

	cmData[ConfigFileName] = config

	return cmData, nil
}

func RBAC(cr *apiv1alpha1.PerconaServerMySQL) (*rbacv1.Role, *rbacv1.RoleBinding, *corev1.ServiceAccount) {
	meta := metav1.ObjectMeta{
		Namespace: cr.Namespace,
		Name:      "percona-server-mysql-operator-orchestrator",
	}

	account := &corev1.ServiceAccount{ObjectMeta: meta}
	account.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ServiceAccount"))

	role := &rbacv1.Role{ObjectMeta: meta}
	role.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind("Role"))
	role.Rules = []rbacv1.PolicyRule{
		{
			APIGroups: []string{corev1.SchemeGroupVersion.Group},
			Resources: []string{"pods"},
			Verbs:     []string{"list", "patch"},
		},
		{
			APIGroups: []string{cr.GroupVersionKind().Group},
			Resources: []string{"perconaservermysqls"},
			Verbs:     []string{"get"},
		},
	}

	binding := &rbacv1.RoleBinding{ObjectMeta: meta}
	binding.SetGroupVersionKind(rbacv1.SchemeGroupVersion.WithKind("RoleBinding"))
	binding.RoleRef = rbacv1.RoleRef{
		APIGroup: rbacv1.SchemeGroupVersion.Group,
		Kind:     role.Kind,
		Name:     role.Name,
	}
	binding.Subjects = []rbacv1.Subject{{
		Kind: account.Kind,
		Name: account.Name,
	}}

	return role, binding, account
}
