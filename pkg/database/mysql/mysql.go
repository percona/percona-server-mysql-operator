package mysql

import (
	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ComponentName    = "mysql"
	DataVolumeName   = "datadir"
	DataMountPath    = "/var/lib/mysql"
	ConfigVolumeName = "config"
	ConfigMountPath  = "/etc/mysql/config"
	CredsVolumeName  = "users"
	CredsMountPath   = "/etc/mysql/mysql-users-secret"
	TLSVolumeName    = "tls"
	TLSMountPath     = "/etc/mysql/mysql-tls-secret"
)

const mySQLPort = 3306

type MySQL struct {
	v2.MySQLSpec

	cluster *v2.PerconaServerForMySQL
}

func New(cr *v2.PerconaServerForMySQL) *MySQL {
	return &MySQL{
		MySQLSpec: cr.Spec.MySQL,
		cluster:   cr,
	}
}

func (m *MySQL) Name() string {
	return m.cluster.Name + "-" + ComponentName
}

func (m *MySQL) Namespace() string {
	return m.cluster.Namespace
}

func (m *MySQL) ServiceName() string {
	return m.Name()
}

func (m *MySQL) PrimaryServiceName() string {
	return m.Name() + "-primary"
}

func IsMySQL(obj client.Object) bool {
	labels := obj.GetLabels()
	return labels[v2.ComponentLabel] == ComponentName
}

func (m *MySQL) MatchLabels() map[string]string {
	return mergeSSMap(m.Labels,
		map[string]string{v2.ComponentLabel: ComponentName},
		m.cluster.Labels())
}

func (m *MySQL) StatefulSet(initImage string) *appsv1.StatefulSet {
	labels := m.MatchLabels()

	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name(),
			Namespace: m.Namespace(),
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &m.Size,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			ServiceName: m.ServiceName(),
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				k8s.PVC(DataVolumeName, m.VolumeSpec),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:            ComponentName + "-init",
							Image:           initImage,
							ImagePullPolicy: m.ImagePullPolicy,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      DataVolumeName,
									MountPath: DataMountPath,
								},
								{
									Name:      CredsVolumeName,
									MountPath: CredsMountPath,
								},
								{
									Name:      TLSVolumeName,
									MountPath: TLSMountPath,
								},
							},
							Command:                  []string{"/ps-init-entrypoint.sh"},
							TerminationMessagePath:   "/dev/termination-log",
							TerminationMessagePolicy: corev1.TerminationMessageReadFile,
							SecurityContext:          m.ContainerSecurityContext,
						},
					},
					Containers: []corev1.Container{m.mysqldContainer()},
					// TerminationGracePeriodSeconds: 30,
					RestartPolicy: corev1.RestartPolicyAlways,
					SchedulerName: "default-scheduler",
					DNSPolicy:     corev1.DNSClusterFirst,
					Volumes: []corev1.Volume{
						{
							Name: CredsVolumeName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: m.cluster.Spec.SecretsName,
								},
							},
						},
						{
							Name: TLSVolumeName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: m.cluster.Spec.SSLSecretName,
								},
							},
						},
					},
					SecurityContext: m.PodSecurityContext,
				},
			},
		},
	}
}

func (m *MySQL) Service() *corev1.Service {
	labels := m.MatchLabels()
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.ServiceName(),
			Namespace: m.Namespace(),
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Ports: []corev1.ServicePort{
				{
					Name: "mysql",
					Port: int32(mySQLPort),
				},
			},
			Selector:                 labels,
			PublishNotReadyAddresses: true,
		},
	}
}

func (m *MySQL) PrimaryService() *corev1.Service {
	labels := m.MatchLabels()
	selector := copySSMap(labels)
	selector[v2.MySQLPrimaryLabel] = "true"

	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.PrimaryServiceName(),
			Namespace: m.Namespace(),
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name: "mysql",
					Port: int32(mySQLPort),
				},
			},
			Selector: selector,
		},
	}
}

func (m *MySQL) mysqldContainer() corev1.Container {
	return corev1.Container{
		Name:            ComponentName,
		Image:           m.Image,
		ImagePullPolicy: m.ImagePullPolicy,
		Env: []corev1.EnvVar{
			{
				Name:  "SERVICE_NAME",
				Value: m.ServiceName(),
			},
			{
				Name:  "CLUSTER_HASH",
				Value: m.cluster.ClusterHash(),
			},
		},
		Ports: []corev1.ContainerPort{
			{
				Name:          "mysql",
				ContainerPort: int32(mySQLPort),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      DataVolumeName,
				MountPath: DataMountPath,
			},
			{
				Name:      CredsVolumeName,
				MountPath: CredsMountPath,
			},
			{
				Name:      TLSVolumeName,
				MountPath: TLSMountPath,
			},
		},
		Command:                  []string{"/var/lib/mysql/ps-entrypoint.sh"},
		Args:                     []string{"mysqld"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          m.ContainerSecurityContext,
		StartupProbe: &corev1.Probe{
			Handler: corev1.Handler{
				Exec: &corev1.ExecAction{
					Command: []string{"/var/lib/mysql/bootstrap"},
				},
			},
			InitialDelaySeconds:           m.StartupProbe.InitialDelaySeconds,
			TimeoutSeconds:                m.StartupProbe.TimeoutSeconds,
			PeriodSeconds:                 m.StartupProbe.PeriodSeconds,
			FailureThreshold:              m.StartupProbe.FailureThreshold,
			SuccessThreshold:              m.StartupProbe.SuccessThreshold,
			TerminationGracePeriodSeconds: m.StartupProbe.TerminationGracePeriodSeconds,
		},
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt(3306),
				},
			},
			InitialDelaySeconds:           m.ReadinessProbe.InitialDelaySeconds,
			TimeoutSeconds:                m.ReadinessProbe.TimeoutSeconds,
			PeriodSeconds:                 m.ReadinessProbe.PeriodSeconds,
			FailureThreshold:              m.ReadinessProbe.FailureThreshold,
			SuccessThreshold:              m.ReadinessProbe.SuccessThreshold,
			TerminationGracePeriodSeconds: m.ReadinessProbe.TerminationGracePeriodSeconds,
		},
	}
}
