package mysql

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

const (
	componentName    = "mysql"
	DataVolumeName   = "datadir"
	DataMountPath    = "/var/lib/mysql"
	CustomConfigKey  = "my.cnf"
	configVolumeName = "config"
	configMountPath  = "/etc/mysql/config"
	credsVolumeName  = "users"
	CredsMountPath   = "/etc/mysql/mysql-users-secret"
	tlsVolumeName    = "tls"
	tlsMountPath     = "/etc/mysql/mysql-tls-secret"
	BackupLogDir     = "/var/log/xtrabackup"
)

const (
	DefaultPort      = 3306
	DefaultGRPort    = 33061
	DefaultAdminPort = 33062
	DefaultXPort     = 33060
	SidecarHTTPPort  = 6033
)

type User struct {
	Username apiv1alpha1.SystemUser
	Password string
	Hosts    []string
}

type Exposer apiv1alpha1.PerconaServerMySQL

func (e *Exposer) Exposed() bool {
	return e.Spec.MySQL.Expose.Enabled
}

func (e *Exposer) Name(index string) string {
	cr := apiv1alpha1.PerconaServerMySQL(*e)
	return Name(&cr) + "-" + index
}

func (e *Exposer) Size() int32 {
	return e.Spec.MySQL.Size
}

func (e *Exposer) Labels() map[string]string {
	cr := apiv1alpha1.PerconaServerMySQL(*e)
	return MatchLabels(&cr)
}

func (e *Exposer) Service(name string) *corev1.Service {
	cr := apiv1alpha1.PerconaServerMySQL(*e)
	return PodService(&cr, cr.Spec.MySQL.Expose.Type, name)
}

func (e *Exposer) SaveOldMeta() bool {
	cr := apiv1alpha1.PerconaServerMySQL(*e)
	return cr.MySQLSpec().Expose.SaveOldMeta()
}

func Name(cr *apiv1alpha1.PerconaServerMySQL) string {
	return cr.Name + "-" + componentName
}

func NamespacedName(cr *apiv1alpha1.PerconaServerMySQL) types.NamespacedName {
	return types.NamespacedName{Name: Name(cr), Namespace: cr.Namespace}
}

func ServiceName(cr *apiv1alpha1.PerconaServerMySQL) string {
	return Name(cr)
}

func UnreadyServiceName(cr *apiv1alpha1.PerconaServerMySQL) string {
	return Name(cr) + "-unready"
}

func ConfigMapName(cr *apiv1alpha1.PerconaServerMySQL) string {
	return Name(cr)
}

func AutoConfigMapName(cr *apiv1alpha1.PerconaServerMySQL) string {
	return "auto-" + Name(cr)
}

func PodName(cr *apiv1alpha1.PerconaServerMySQL, idx int) string {
	return fmt.Sprintf("%s-%d", Name(cr), idx)
}

func FQDN(cr *apiv1alpha1.PerconaServerMySQL, idx int) string {
	return fmt.Sprintf("%s.%s.%s", PodName(cr, idx), ServiceName(cr), cr.Namespace)
}

func MatchLabels(cr *apiv1alpha1.PerconaServerMySQL) map[string]string {
	return util.SSMapMerge(cr.MySQLSpec().Labels,
		map[string]string{apiv1alpha1.ComponentLabel: componentName},
		cr.Labels())
}

func StatefulSet(cr *apiv1alpha1.PerconaServerMySQL, initImage, configHash string, secret *corev1.Secret) *appsv1.StatefulSet {
	labels := MatchLabels(cr)
	spec := cr.MySQLSpec()
	replicas := spec.Size
	t := true

	annotations := make(map[string]string)
	if configHash != "" {
		annotations["percona.com/configuration-hash"] = configHash
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
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			ServiceName:          ServiceName(cr),
			VolumeClaimTemplates: volumeClaimTemplates(spec),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						k8s.InitContainer(
							componentName,
							initImage,
							spec.ImagePullPolicy,
							spec.ContainerSecurityContext,
						),
					},
					Containers:                    containers(cr, secret),
					ServiceAccountName:            cr.Spec.MySQL.ServiceAccountName,
					NodeSelector:                  cr.Spec.MySQL.NodeSelector,
					Tolerations:                   cr.Spec.MySQL.Tolerations,
					Affinity:                      spec.GetAffinity(labels),
					ImagePullSecrets:              spec.ImagePullSecrets,
					TerminationGracePeriodSeconds: spec.TerminationGracePeriodSeconds,
					PriorityClassName:             spec.PriorityClassName,
					RuntimeClassName:              spec.RuntimeClassName,
					RestartPolicy:                 corev1.RestartPolicyAlways,
					SchedulerName:                 spec.SchedulerName,
					DNSPolicy:                     corev1.DNSClusterFirst,
					Volumes: append(
						[]corev1.Volume{
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
									Projected: &corev1.ProjectedVolumeSource{
										Sources: []corev1.VolumeProjection{
											{
												ConfigMap: &corev1.ConfigMapProjection{
													LocalObjectReference: corev1.LocalObjectReference{
														Name: ConfigMapName(cr),
													},
													Items: []corev1.KeyToPath{
														{
															Key:  CustomConfigKey,
															Path: "my-config.cnf",
														},
													},
													Optional: &t,
												},
											},
											{
												ConfigMap: &corev1.ConfigMapProjection{
													LocalObjectReference: corev1.LocalObjectReference{
														Name: AutoConfigMapName(cr),
													},
													Items: []corev1.KeyToPath{
														{
															Key:  CustomConfigKey,
															Path: "auto-config.cnf",
														},
													},
													Optional: &t,
												},
											},
											{
												Secret: &corev1.SecretProjection{
													LocalObjectReference: corev1.LocalObjectReference{
														Name: ConfigMapName(cr),
													},
													Items: []corev1.KeyToPath{
														{
															Key:  CustomConfigKey,
															Path: "my-secret.cnf",
														},
													},
													Optional: &t,
												},
											},
										},
									},
								},
							},
							{
								Name: "backup-logs",
								VolumeSource: corev1.VolumeSource{
									EmptyDir: &corev1.EmptyDirVolumeSource{},
								},
							},
						},
						spec.SidecarVolumes...,
					),
					SecurityContext: spec.PodSecurityContext,
				},
			},
		},
	}
}

func volumeClaimTemplates(spec *apiv1alpha1.MySQLSpec) []corev1.PersistentVolumeClaim {
	pvcs := []corev1.PersistentVolumeClaim{
		k8s.PVC(DataVolumeName, spec.VolumeSpec),
	}
	for _, p := range spec.SidecarPVCs {
		pvcs = append(pvcs, corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: p.Name},
			Spec:       p.Spec,
		})
	}

	return pvcs
}

func servicePorts(cr *apiv1alpha1.PerconaServerMySQL) []corev1.ServicePort {
	ports := []corev1.ServicePort{
		{
			Name: componentName,
			Port: DefaultPort,
		},
		{
			Name: "mysql-admin",
			Port: DefaultAdminPort,
		},
		{
			Name: "mysqlx",
			Port: DefaultXPort,
		},
		{
			Name: "http",
			Port: SidecarHTTPPort,
		},
	}

	if cr.Spec.MySQL.IsGR() {
		ports = append(ports, corev1.ServicePort{Name: componentName + "-gr", Port: DefaultGRPort})
	}

	return ports
}

func containerPorts(cr *apiv1alpha1.PerconaServerMySQL) []corev1.ContainerPort {
	ports := []corev1.ContainerPort{
		{
			Name:          componentName,
			ContainerPort: DefaultPort,
		},
		{
			Name:          "mysql-admin",
			ContainerPort: DefaultAdminPort,
		},
		{
			Name:          "mysqlx",
			ContainerPort: DefaultXPort,
		},
	}

	if cr.Spec.MySQL.IsGR() {
		ports = append(ports, corev1.ContainerPort{Name: componentName + "-gr", ContainerPort: DefaultGRPort})
	}

	return ports
}

func UnreadyService(cr *apiv1alpha1.PerconaServerMySQL) *corev1.Service {
	labels := MatchLabels(cr)

	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      UnreadyServiceName(cr),
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP:                "None",
			Ports:                    servicePorts(cr),
			Selector:                 labels,
			PublishNotReadyAddresses: true,
		},
	}
}

func HeadlessService(cr *apiv1alpha1.PerconaServerMySQL) *corev1.Service {
	labels := MatchLabels(cr)
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceName(cr),
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:                     corev1.ServiceTypeClusterIP,
			ClusterIP:                "None",
			Ports:                    servicePorts(cr),
			Selector:                 labels,
			PublishNotReadyAddresses: cr.Spec.MySQL.IsGR(),
		},
	}
}

func PodService(cr *apiv1alpha1.PerconaServerMySQL, t corev1.ServiceType, podName string) *corev1.Service {
	expose := cr.Spec.MySQL.Expose

	labels := MatchLabels(cr)
	labels[apiv1alpha1.ExposedLabel] = "true"
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
			Type:                     t,
			Selector:                 selector,
			Ports:                    servicePorts(cr),
			LoadBalancerSourceRanges: loadBalancerSourceRanges,
			InternalTrafficPolicy:    expose.InternalTrafficPolicy,
			ExternalTrafficPolicy:    externalTrafficPolicy,
		},
	}
}

func containers(cr *apiv1alpha1.PerconaServerMySQL, secret *corev1.Secret) []corev1.Container {
	containers := []corev1.Container{mysqldContainer(cr)}

	if backup := cr.Spec.Backup; backup != nil && backup.Enabled {
		containers = append(containers, backupContainer(cr))
	}

	if toolkit := cr.Spec.Toolkit; toolkit != nil && cr.Spec.MySQL.IsAsync() && cr.OrchestratorEnabled() {
		containers = append(containers, heartbeatContainer(cr))
	}

	if cr.PMMEnabled(secret) {
		containers = append(containers, pmmContainer(cr, secret))
	}

	return appendUniqueContainers(containers, cr.Spec.MySQL.Sidecars...)
}

func mysqldContainer(cr *apiv1alpha1.PerconaServerMySQL) corev1.Container {
	spec := cr.MySQLSpec()

	container := corev1.Container{
		Name:            componentName,
		Image:           spec.Image,
		ImagePullPolicy: spec.ImagePullPolicy,
		Resources:       spec.Resources,
		Env: []corev1.EnvVar{
			{
				Name:  "MONITOR_HOST",
				Value: "%",
			},
			{
				Name:  "SERVICE_NAME",
				Value: ServiceName(cr),
			},
			{
				Name:  "SERVICE_NAME_UNREADY",
				Value: UnreadyServiceName(cr),
			},
			{
				Name:  "CLUSTER_HASH",
				Value: cr.ClusterHash(),
			},
			{
				Name:  "INNODB_CLUSTER_NAME",
				Value: cr.InnoDBClusterName(),
			},
			{
				Name:  "CR_UID",
				Value: string(cr.UID),
			},
			{
				Name:  "CLUSTER_TYPE",
				Value: string(cr.Spec.MySQL.ClusterType),
			},
		},
		Ports: containerPorts(cr),
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      apiv1alpha1.BinVolumeName,
				MountPath: apiv1alpha1.BinVolumePath,
			},
			{
				Name:      DataVolumeName,
				MountPath: DataMountPath,
			},
			{
				Name:      credsVolumeName,
				MountPath: CredsMountPath,
			},
			{
				Name:      tlsVolumeName,
				MountPath: tlsMountPath,
			},
			{
				Name:      configVolumeName,
				MountPath: configMountPath,
			},
		},
		Command:                  []string{"/opt/percona/ps-entrypoint.sh"},
		Args:                     []string{"mysqld"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          spec.ContainerSecurityContext,
		LivenessProbe:            k8s.ExecProbe(spec.LivenessProbe, []string{"/opt/percona/healthcheck", "liveness"}),
		ReadinessProbe:           k8s.ExecProbe(spec.ReadinessProbe, []string{"/opt/percona/healthcheck", "readiness"}),
		StartupProbe:             k8s.ExecProbe(spec.StartupProbe, []string{"/opt/percona/bootstrap"}),
	}

	return container
}

func backupContainer(cr *apiv1alpha1.PerconaServerMySQL) corev1.Container {
	return corev1.Container{
		Name:            "xtrabackup",
		Image:           cr.Spec.Backup.Image,
		ImagePullPolicy: cr.Spec.Backup.ImagePullPolicy,
		Env:             []corev1.EnvVar{},
		Ports: []corev1.ContainerPort{
			{
				Name:          "http",
				ContainerPort: SidecarHTTPPort,
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      apiv1alpha1.BinVolumeName,
				MountPath: apiv1alpha1.BinVolumePath,
			},
			{
				Name:      DataVolumeName,
				MountPath: DataMountPath,
			},
			{
				Name:      credsVolumeName,
				MountPath: CredsMountPath,
			},
			{
				Name:      "backup-logs",
				MountPath: BackupLogDir,
			},
		},
		Command:                  []string{"/opt/percona/sidecar"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}
}

func heartbeatContainer(cr *apiv1alpha1.PerconaServerMySQL) corev1.Container {
	return corev1.Container{
		Name:            "pt-heartbeat",
		Image:           cr.Spec.Toolkit.Image,
		ImagePullPolicy: cr.Spec.Toolkit.ImagePullPolicy,
		SecurityContext: cr.Spec.Toolkit.ContainerSecurityContext,
		Resources:       cr.Spec.Toolkit.Resources,
		Env: []corev1.EnvVar{
			{
				Name: "HEARTBEAT_PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: k8s.SecretKeySelector(cr.InternalSecretName(), string(apiv1alpha1.UserHeartbeat)),
				},
			},
		},
		Ports: []corev1.ContainerPort{},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      apiv1alpha1.BinVolumeName,
				MountPath: apiv1alpha1.BinVolumePath,
			},
			{
				Name:      DataVolumeName,
				MountPath: DataMountPath,
			},
			{
				Name:      credsVolumeName,
				MountPath: CredsMountPath,
			},
		},
		Command:                  []string{"/opt/percona/heartbeat-entrypoint.sh"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}
}

func pmmContainer(cr *apiv1alpha1.PerconaServerMySQL, secret *corev1.Secret) corev1.Container {
	ports := []corev1.ContainerPort{{ContainerPort: 7777}}
	for port := 30100; port <= 30105; port++ {
		ports = append(ports, corev1.ContainerPort{ContainerPort: int32(port)})
	}

	user := "api_key"
	passwordKey := string(apiv1alpha1.UserPMMServerKey)

	pmmSpec := cr.PMMSpec()

	return corev1.Container{
		Name:            "pmm-client",
		Image:           pmmSpec.Image,
		ImagePullPolicy: pmmSpec.ImagePullPolicy,
		SecurityContext: pmmSpec.ContainerSecurityContext,
		Ports:           ports,
		Resources:       pmmSpec.Resources,
		Env: []corev1.EnvVar{
			{
				Name: "POD_NAME",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.name",
					},
				},
			},
			{
				Name: "POD_NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.namespace",
					},
				},
			},
			{
				Name:  "CLUSTER_NAME",
				Value: cr.Name,
			},
			{
				Name:  "CLIENT_PORT_LISTEN",
				Value: "7777",
			},
			{
				Name:  "CLIENT_PORT_MIN",
				Value: "30100",
			},
			{
				Name:  "CLIENT_PORT_MAX",
				Value: "30105",
			},
			{
				Name:  "PMM_AGENT_SERVER_ADDRESS",
				Value: pmmSpec.ServerHost,
			},
			{
				Name:  "PMM_AGENT_SERVER_USERNAME",
				Value: user,
			},
			{
				Name: "PMM_AGENT_SERVER_PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: k8s.SecretKeySelector(secret.Name, passwordKey),
				},
			},
			{
				Name:  "PMM_SERVER",
				Value: pmmSpec.ServerHost,
			},
			{
				Name:  "PMM_USER",
				Value: user,
			},
			{
				Name: "PMM_PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: k8s.SecretKeySelector(secret.Name, passwordKey),
				},
			},
			{
				Name:  "PMM_AGENT_LISTEN_PORT",
				Value: "7777",
			},
			{
				Name:  "PMM_AGENT_PORTS_MIN",
				Value: "30100",
			},
			{
				Name:  "PMM_AGENT_PORTS_MAX",
				Value: "30105",
			},
			{
				Name:  "PMM_AGENT_CONFIG_FILE",
				Value: "/usr/local/percona/pmm2/config/pmm-agent.yaml",
			},
			{
				Name:  "PMM_AGENT_SERVER_INSECURE_TLS",
				Value: "1",
			},
			{
				Name:  "PMM_AGENT_LISTEN_ADDRESS",
				Value: "0.0.0.0",
			},
			{
				Name:  "PMM_AGENT_SETUP_NODE_NAME",
				Value: "$(POD_NAMESPACE)-$(POD_NAME)",
			},
			{
				Name:  "PMM_AGENT_SETUP_METRICS_MODE",
				Value: "push",
			},
			{
				Name:  "PMM_AGENT_SETUP",
				Value: "1",
			},
			{
				Name:  "PMM_AGENT_SETUP_FORCE",
				Value: "1",
			},
			{
				Name:  "PMM_AGENT_SETUP_NODE_TYPE",
				Value: "container",
			},
			{
				Name:  "PMM_AGENT_PRERUN_SCRIPT",
				Value: "pmm-admin status --wait=10s;\npmm-admin add ${DB_TYPE} ${PMM_ADMIN_CUSTOM_PARAMS} --skip-connection-check --metrics-mode=${PMM_AGENT_SETUP_METRICS_MODE} --username=${DB_USER} --password=${DB_PASSWORD} --cluster=${CLUSTER_NAME} --service-name=${PMM_AGENT_SETUP_NODE_NAME} --host=${POD_NAME} --port=${DB_PORT} ${DB_ARGS};\npmm-admin annotate --service-name=${PMM_AGENT_SETUP_NODE_NAME} 'Service restarted'",
			},
			{
				Name:  "PMM_AGENT_SIDECAR",
				Value: "true",
			},
			{
				Name:  "PMM_AGENT_SIDECAR_SLEEP",
				Value: "5",
			},
			{
				Name:  "DB_CLUSTER",
				Value: cr.Name,
			},
			{
				Name:  "DB_TYPE",
				Value: componentName,
			},
			{
				Name:  "DB_HOST",
				Value: "localhost",
			},
			{
				Name:  "DB_PORT",
				Value: "33062",
			},
			{
				Name:  "DB_USER",
				Value: string(apiv1alpha1.UserMonitor),
			},
			{
				Name: "DB_PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: k8s.SecretKeySelector(secret.Name, string(apiv1alpha1.UserMonitor)),
				},
			},
			{
				Name:  "DB_ARGS",
				Value: "--query-source=perfschema",
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      apiv1alpha1.BinVolumeName,
				MountPath: apiv1alpha1.BinVolumePath,
			},
			{
				Name:      DataVolumeName,
				MountPath: DataMountPath,
			},
		},
	}
}

func appendUniqueContainers(containers []corev1.Container, more ...corev1.Container) []corev1.Container {
	if len(more) == 0 {
		return containers
	}

	exists := make(map[string]bool)
	for i := range containers {
		exists[containers[i].Name] = true
	}

	for i := range more {
		name := more[i].Name
		if exists[name] {
			continue
		}

		containers = append(containers, more[i])
		exists[name] = true
	}

	return containers
}
