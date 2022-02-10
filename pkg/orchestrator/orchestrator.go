package orchestrator

import (
	"fmt"
	"path/filepath"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

const (
	componentName   = "orc"
	defaultWebPort  = 3000
	defaultRaftPort = 10008
	dataVolumeName  = "datadir"
	DataMountPath   = "/var/lib/orchestrator"
	// configVolumeName = "config"
	// configMountPath  = "/etc/orchestrator"
	credsVolumeName = "users"
	CredsMountPath  = "/etc/orchestrator/orchestrator-users-secret"
	tlsVolumeName   = "tls"
	tlsMountPath    = "/etc/orchestrator/ssl"
)

// Name returns component name
func Name(cr *apiv1alpha1.PerconaServerMySQL) string {
	return cr.Name + "-" + componentName
}

func NamespacedName(cr *apiv1alpha1.PerconaServerMySQL) types.NamespacedName {
	return types.NamespacedName{Name: Name(cr), Namespace: cr.Namespace}
}

func ServiceName(cr *apiv1alpha1.PerconaServerMySQL) string {
	return Name(cr)
}

func APIHost(serviceName string) string {
	return fmt.Sprintf("http://%s:%d", serviceName, defaultWebPort)
}

// Labels returns labels of orchestrator
func Labels(cr *apiv1alpha1.PerconaServerMySQL) map[string]string {
	return cr.OrchestratorSpec().Labels
}

func MatchLabels(cr *apiv1alpha1.PerconaServerMySQL) map[string]string {
	return util.SSMapMerge(Labels(cr),
		map[string]string{apiv1alpha1.ComponentLabel: componentName},
		cr.Labels())
}

func StatefulSet(cr *apiv1alpha1.PerconaServerMySQL) *appsv1.StatefulSet {
	labels := MatchLabels(cr)
	spec := cr.OrchestratorSpec()
	Replicas := spec.Size

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
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				k8s.PVC(dataVolumeName, spec.VolumeSpec),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers:       containers(cr),
					Affinity:         spec.GetAffinity(labels),
					ImagePullSecrets: spec.ImagePullSecrets,
					// TerminationGracePeriodSeconds: 30,
					RestartPolicy: corev1.RestartPolicyAlways,
					SchedulerName: "default-scheduler",
					DNSPolicy:     corev1.DNSClusterFirst,
					Volumes: []corev1.Volume{
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
					},
					SecurityContext: spec.PodSecurityContext,
				},
			},
		},
	}
}

func containers(cr *apiv1alpha1.PerconaServerMySQL) []corev1.Container {
	sidecars := sidecarContainers(cr)
	containers := make([]corev1.Container, 1, len(sidecars)+1)
	containers[0] = container(cr)
	return append(containers, sidecars...)
}

func container(cr *apiv1alpha1.PerconaServerMySQL) corev1.Container {
	return corev1.Container{
		Name:            componentName,
		Image:           cr.Spec.Orchestrator.Image,
		ImagePullPolicy: cr.Spec.Orchestrator.ImagePullPolicy,
		Resources:       cr.Spec.Orchestrator.Resources,
		Env: []corev1.EnvVar{
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
				Value: "false",
			},
		},
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
			Handler: corev1.Handler{
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
			Handler: corev1.Handler{
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
			Args: []string{
				"/usr/bin/peer-list",
				"-on-change=/usr/bin/add_mysql_nodes.sh",
				"-service=$(MYSQL_SERVICE)",
			},
			TerminationMessagePath:   "/dev/termination-log",
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
			SecurityContext:          cr.Spec.Orchestrator.ContainerSecurityContext,
		},
	}
}

func containerMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      dataVolumeName,
			MountPath: DataMountPath,
		},
		{
			Name:      tlsVolumeName,
			MountPath: tlsMountPath,
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
