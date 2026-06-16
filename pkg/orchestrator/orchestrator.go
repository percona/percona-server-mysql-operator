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

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

const (
	AppName                = "orchestrator"
	componentShortName     = "orc"
	defaultWebPort         = 3000
	defaultRaftPort        = 10008
	customConfigVolumeName = "custom"
	configVolumeName       = "config"
	configMountPath        = "/etc/orchestrator/config"
	customConfigMountPath  = "/etc/orchestrator/custom"
	configFileKey          = "orchestrator.conf.json"
	credsVolumeName        = "users"
	CredsMountPath         = "/etc/orchestrator/orchestrator-users-secret"
	tlsVolumeName          = "tls"
	tlsMountPath           = "/etc/orchestrator/ssl"
)

type Exposer apiv1.PerconaServerMySQL

func (e *Exposer) Exposed() bool {
	cr := apiv1.PerconaServerMySQL(*e)
	return cr.OrchestratorEnabled()
}

func (e *Exposer) Name(index string) string {
	cr := apiv1.PerconaServerMySQL(*e)
	return Name(&cr) + "-" + index
}

func (e *Exposer) Size() int32 {
	return e.Spec.Orchestrator.Size
}

func (e *Exposer) MatchLabels() map[string]string {
	cr := apiv1.PerconaServerMySQL(*e)
	return MatchLabels(&cr)
}

func (e *Exposer) Service(name string) *corev1.Service {
	cr := apiv1.PerconaServerMySQL(*e)
	return PodService(&cr, cr.Spec.Orchestrator.Expose.Type, name)
}

func (e *Exposer) SaveOldMeta() bool {
	cr := apiv1.PerconaServerMySQL(*e)
	return cr.OrchestratorSpec().Expose.SaveOldMeta()
}

// Name returns component name
func Name(cr *apiv1.PerconaServerMySQL) string {
	return cr.Name + "-" + componentShortName
}

func NamespacedName(cr *apiv1.PerconaServerMySQL) types.NamespacedName {
	return types.NamespacedName{Name: Name(cr), Namespace: cr.Namespace}
}

func ServiceName(cr *apiv1.PerconaServerMySQL) string {
	return Name(cr)
}

func ConfigMapName(cr *apiv1.PerconaServerMySQL) string {
	return Name(cr)
}

func PodName(cr *apiv1.PerconaServerMySQL, idx int) string {
	return fmt.Sprintf("%s-%d", Name(cr), idx)
}

func FQDN(cr *apiv1.PerconaServerMySQL, idx int) string {
	// TODO: DNS suffix
	return fmt.Sprintf("%s.%s.svc", PodName(cr, idx), cr.Namespace)
}

// Labels returns labels of orchestrator
func Labels(cr *apiv1.PerconaServerMySQL) map[string]string {
	return util.SSMapMerge(cr.GlobalLabels(), cr.OrchestratorSpec().Labels, MatchLabels(cr))
}

func MatchLabels(cr *apiv1.PerconaServerMySQL) map[string]string {
	return cr.Labels(AppName, naming.ComponentOrchestrator)
}

func StatefulSet(cr *apiv1.PerconaServerMySQL, initImage, configHash, tlsHash string) *appsv1.StatefulSet {
	selector := MatchLabels(cr)
	spec := cr.OrchestratorSpec()
	Replicas := spec.Size

	annotations := make(map[string]string)
	if cr.CompareVersion("0.12.0") >= 0 {
		if configHash != "" {
			annotations[string(naming.AnnotationConfigHash)] = configHash
		}
	}
	if tlsHash != "" {
		annotations[string(naming.AnnotationTLSHash)] = tlsHash
	}

	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        Name(cr),
			Namespace:   cr.Namespace,
			Labels:      Labels(cr),
			Annotations: cr.GlobalAnnotations(),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &Replicas,
			ServiceName: Name(cr),
			Selector: &metav1.LabelSelector{
				MatchLabels: selector,
			},
			UpdateStrategy: updateStrategy(cr),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      Labels(cr),
					Annotations: util.SSMapMerge(cr.GlobalAnnotations(), annotations),
				},
				Spec: spec.Core(
					selector,
					volumes(cr),
					[]corev1.Container{
						k8s.InitContainer(
							cr,
							AppName,
							initImage,
							spec.InitContainer,
							spec.ImagePullPolicy,
							spec.ContainerSecurityContext,
							spec.Resources,
							nil,
						),
					},
					containers(cr),
				),
			},
		},
	}
}

func volumes(cr *apiv1.PerconaServerMySQL) []corev1.Volume {
	return []corev1.Volume{
		{
			Name: apiv1.BinVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: configVolumeName,
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
			Name: customConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: ConfigMapName(cr),
					},
				},
			},
		},
	}
}

func updateStrategy(cr *apiv1.PerconaServerMySQL) appsv1.StatefulSetUpdateStrategy {
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

func containers(cr *apiv1.PerconaServerMySQL) []corev1.Container {
	sidecars := sidecarContainers(cr)
	containers := make([]corev1.Container, 1, len(sidecars)+1)
	containers[0] = container(cr)
	return append(containers, sidecars...)
}

func container(cr *apiv1.PerconaServerMySQL) corev1.Container {
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
	env = append(env, apiAuthEnv(cr)...)

	return corev1.Container{
		Name:            AppName,
		Image:           cr.Spec.Orchestrator.Image,
		ImagePullPolicy: cr.Spec.Orchestrator.ImagePullPolicy,
		Resources:       cr.Spec.Orchestrator.Resources,
		Command:         []string{"/opt/percona/orc-entrypoint.sh"},
		Args:            []string{"/usr/local/orchestrator/orchestrator", "-config", "/etc/orchestrator/config/orchestrator.conf.json", "http"},
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
		LivenessProbe:            apiProbe(cr, "/api/lb-check", 10),
		ReadinessProbe:           apiProbe(cr, "/api/health", 30),
	}
}

// apiAuthEnv gates HTTP API auth from crVersion 1.2.0. Every container running
// orc-entrypoint.sh must set it identically, else one starting later rewrites the
// shared config without auth.
func apiAuthEnv(cr *apiv1.PerconaServerMySQL) []corev1.EnvVar {
	if cr.CompareVersion("1.2.0") >= 0 {
		return []corev1.EnvVar{{Name: "ORC_API_AUTH", Value: "true"}}
	}
	return nil
}

// apiProbe builds an Orchestrator HTTP API probe. With auth enabled the health
// endpoints are gated too, so the probe execs curl as the readonly user
// (any password) instead of an httpGet that would get 401.
func apiProbe(cr *apiv1.PerconaServerMySQL, path string, initialDelay int32) *corev1.Probe {
	probe := &corev1.Probe{
		InitialDelaySeconds: initialDelay,
		TimeoutSeconds:      3,
		PeriodSeconds:       5,
		FailureThreshold:    3,
		SuccessThreshold:    1,
	}

	if cr.CompareVersion("1.2.0") >= 0 {
		curl := fmt.Sprintf(`curl -sf -u "readonly:readonly" "localhost:%d%s"`, defaultWebPort, path)
		probe.ProbeHandler = corev1.ProbeHandler{
			Exec: &corev1.ExecAction{Command: []string{"sh", "-c", curl}},
		}
		return probe
	}

	probe.ProbeHandler = corev1.ProbeHandler{
		HTTPGet: &corev1.HTTPGetAction{
			Path: path,
			Port: intstr.FromString("web"),
		},
	}
	return probe
}

func sidecarContainers(cr *apiv1.PerconaServerMySQL) []corev1.Container {
	serviceName := mysql.ServiceName(cr)

	return []corev1.Container{
		{
			Name:            "mysql-monit",
			Image:           cr.Spec.Orchestrator.Image,
			ImagePullPolicy: cr.Spec.Orchestrator.ImagePullPolicy,
			Env: append([]corev1.EnvVar{
				{
					Name:  "ORC_SERVICE",
					Value: serviceName,
				},
				{
					Name:  "MYSQL_SERVICE",
					Value: serviceName,
				},
			}, apiAuthEnv(cr)...),
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
			Name:      apiv1.BinVolumeName,
			MountPath: apiv1.BinVolumePath,
		},
		{
			Name:      tlsVolumeName,
			MountPath: tlsMountPath,
		},
		{
			Name:      customConfigVolumeName,
			MountPath: customConfigMountPath,
		},
		{
			Name:      configVolumeName,
			MountPath: configMountPath,
		},
		{
			Name:      credsVolumeName,
			MountPath: filepath.Join(CredsMountPath, string(apiv1.UserOrchestrator)),
			SubPath:   string(apiv1.UserOrchestrator),
		},
	}
}

func Service(cr *apiv1.PerconaServerMySQL) *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        ServiceName(cr),
			Namespace:   cr.Namespace,
			Labels:      util.SSMapMerge(cr.GlobalLabels(), MatchLabels(cr)),
			Annotations: cr.GlobalAnnotations(),
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

func PodService(cr *apiv1.PerconaServerMySQL, t corev1.ServiceType, podName string) *corev1.Service {
	expose := cr.Spec.Orchestrator.Expose

	labels := MatchLabels(cr)
	labels[naming.LabelExposed] = "true"
	labels = util.SSMapMerge(cr.GlobalLabels(), expose.Labels, labels)

	selector := MatchLabels(cr)
	selector["statefulset.kubernetes.io/pod-name"] = podName

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
			Annotations: util.SSMapMerge(cr.GlobalAnnotations(), expose.Annotations),
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

func RaftNodes(cr *apiv1.PerconaServerMySQL) []string {
	nodes := make([]string, cr.Spec.Orchestrator.Size)

	for i := 0; i < int(cr.Spec.Orchestrator.Size); i++ {
		nodes[i] = fmt.Sprintf("%s:%d", FQDN(cr, i), 10008)
	}

	return nodes
}

func ExistingNodes(cmap *corev1.ConfigMap) ([]string, error) {
	cfg, ok := cmap.Data[configFileKey]
	if !ok {
		return nil, errors.Errorf("key %s not found in ConfigMap", configFileKey)
	}

	config := make(map[string]any, 0)
	if err := json.Unmarshal([]byte(cfg), &config); err != nil {
		return nil, errors.Wrap(err, "unmarshal ConfigMap data to json")
	}

	nodes, ok := config["RaftNodes"]
	if !ok {
		return nil, errors.New("key RaftNodes not found in ConfigMap")
	}

	nodesSlice, ok := nodes.([]any)
	if !ok {
		return nil, errors.Errorf("invalid RaftNodes: expected []any, got %T", nodes)
	}

	existingNodes := make([]string, 0, len(nodesSlice))
	for _, v := range nodesSlice {
		node, ok := v.(string)
		if !ok {
			return nil, errors.Errorf("invalid node: expected string, got %T", v)
		}

		existingNodes = append(existingNodes, node)
	}

	return existingNodes, nil
}

func ConfigMap(cr *apiv1.PerconaServerMySQL) (*corev1.ConfigMap, error) {
	config, err := ConfigMapData(cr)
	if err != nil {
		return nil, errors.Wrap(err, "get orchestrator config")
	}
	return k8s.ConfigMap(cr, ConfigMapName(cr), configFileKey, config, naming.ComponentOrchestrator), nil
}

// reservedOrchestratorConfigKeys must not be overridable via
// spec.orchestrator.configuration. They fall into three groups: operator-managed
// (set in ConfigMapData), injected per-pod by the entrypoint, and baked defaults
// the operator's own integration depends on. Overriding any would break raft
// membership, per-pod identity, topology TLS, API auth, or operator functionality
// such as primary-pod labelling. Tuning knobs in build/orchestrator.conf.json
// (poll/recovery intervals, lag thresholds, filters, etc.) stay user-overridable.
var reservedOrchestratorConfigKeys = map[string]bool{
	// operator-managed (set in ConfigMapData)
	"RaftNodes":             true,
	"RaftEnabledSingleNode": true,
	// entrypoint-injected per-pod (orc-entrypoint.sh)
	"HTTPAdvertise":                  true,
	"RaftAdvertise":                  true,
	"RaftBind":                       true,
	"RaftEnabled":                    true,
	"MySQLTopologyUseMutualTLS":      true,
	"MySQLTopologySSLSkipVerify":     true,
	"MySQLTopologySSLPrivateKeyFile": true,
	"MySQLTopologySSLCertFile":       true,
	"MySQLTopologySSLCAFile":         true,
	"AuthenticationMethod":           true,
	"HTTPAuthUser":                   true,
	"HTTPAuthPassword":               true,
	// failover hooks that label the primary pod via orc-handler
	"PostFailoverProcesses":                   true,
	"PostMasterFailoverProcesses":             true,
	"PostIntermediateMasterFailoverProcesses": true,
	"PostGracefulTakeoverProcesses":           true,
	// alias/hostname detection the operator relies on to map instances to pods
	"DetectClusterAliasQuery":    true,
	"DetectInstanceAliasQuery":   true,
	// operator-managed endpoints, paths and state
	"ListenAddress":                      true,
	"MySQLTopologyCredentialsConfigFile": true,
	"RaftDataDir":                        true,
	"SQLite3DataFile":                    true,
	"BackendDB":                          true,
	// failover/HA semantics the operator assumes
	"ApplyMySQLPromotionAfterMasterFailover":    true,
	"MasterFailoverDetachReplicaMasterHost":     true,
	"DetachLostReplicasAfterMasterFailover":     true,
	"FailMasterPromotionIfSQLThreadNotUpToDate": true,
}

func ConfigMapData(cr *apiv1.PerconaServerMySQL) (string, error) {
	config := make(map[string]interface{}, 0)

	config["RaftNodes"] = RaftNodes(cr)

	if cr.CompareVersion("0.12.0") >= 0 {
		config["RaftEnabledSingleNode"] = false
		if cr.Spec.Orchestrator.Size == 1 {
			config["RaftEnabledSingleNode"] = true
		}
	}

	if cr.CompareVersion("1.2.0") >= 0 && cr.Spec.SSLSecretName != "" {
		config["MySQLTopologyUseMutualTLS"] = true
		config["MySQLTopologySSLSkipVerify"] = true
		config["MySQLTopologySSLPrivateKeyFile"] = filepath.Join(tlsMountPath, "tls.key")
		config["MySQLTopologySSLCertFile"] = filepath.Join(tlsMountPath, "tls.crt")
		config["MySQLTopologySSLCAFile"] = filepath.Join(tlsMountPath, "ca.crt")
	}

	if cfg := cr.Spec.Orchestrator.Configuration; cfg != "" {
		userConfig := make(map[string]interface{})
		if err := json.Unmarshal([]byte(cfg), &userConfig); err != nil {
			return "", errors.Wrap(err, "unmarshal spec.orchestrator.configuration: must be a JSON object")
		}
		if userConfig == nil {
			return "", errors.New("spec.orchestrator.configuration: must be a JSON object")
		}
		for k, v := range userConfig {
			if reservedOrchestratorConfigKeys[k] {
				continue
			}
			config[k] = v
		}
	}

	configJson, err := json.Marshal(config)
	if err != nil {
		return "", errors.Wrap(err, "marshal orchestrator raft nodes to json")
	}

	return string(configJson), nil
}

func RBAC(cr *apiv1.PerconaServerMySQL) (*rbacv1.Role, *rbacv1.RoleBinding, *corev1.ServiceAccount) {
	meta := metav1.ObjectMeta{
		Namespace:   cr.Namespace,
		Name:        "percona-server-mysql-operator-orchestrator",
		Labels:      cr.GlobalLabels(),
		Annotations: cr.GlobalAnnotations(),
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
