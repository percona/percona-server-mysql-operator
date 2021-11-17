package mysql

import (
	v2 "github.com/percona/percona-server-mysql-operator/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	componentName  = "mysql"
	dataVolumeName = "datadir"
	DataMountPath  = "/var/lib/mysql"
	// configVolumeName = "config"
	// configMountPath  = "/etc/mysql/config"
	credsVolumeName = "users"
	CredsMountPath  = "/etc/mysql/mysql-users-secret"
	tlsVolumeName   = "tls"
	tlsMountPath    = "/etc/mysql/mysql-tls-secret"
)

const (
	DefaultPort      = 3306
	DefaultAdminPort = 33062
)

func Name(cr *v2.PerconaServerForMySQL) string {
	return cr.Name + "-" + componentName
}

func ServiceName(cr *v2.PerconaServerForMySQL) string {
	return Name(cr)
}

func PrimaryServiceName(cr *v2.PerconaServerForMySQL) string {
	return Name(cr) + "-primary"
}

func podSpec(cr *v2.PerconaServerForMySQL) *v2.MySQLSpec {
	return &cr.Spec.MySQL
}

func MatchLabels(cr *v2.PerconaServerForMySQL) map[string]string {
	return util.SSMapMerge(cr.MySQLSpec().Labels,
		map[string]string{v2.ComponentLabel: componentName},
		cr.Labels())
}

func StatefulSet(cr *v2.PerconaServerForMySQL, initImage string) *appsv1.StatefulSet {
	labels := MatchLabels(cr)
	spec := cr.MySQLSpec()
	Replicas := spec.Size

	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      Name(cr),
			Namespace: k8s.Namespace(cr),
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			ServiceName: ServiceName(cr),
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				k8s.PVC(dataVolumeName, spec.VolumeSpec),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name:            componentName + "-init",
							Image:           initImage,
							ImagePullPolicy: spec.ImagePullPolicy,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      dataVolumeName,
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
							},
							Command:                  []string{"/ps-init-entrypoint.sh"},
							TerminationMessagePath:   "/dev/termination-log",
							TerminationMessagePolicy: corev1.TerminationMessageReadFile,
							SecurityContext:          spec.ContainerSecurityContext,
						},
					},
					Containers: []corev1.Container{mysqldContainer(cr)},
					// TerminationGracePeriodSeconds: 30,
					RestartPolicy: corev1.RestartPolicyAlways,
					SchedulerName: "default-scheduler",
					DNSPolicy:     corev1.DNSClusterFirst,
					Volumes: []corev1.Volume{
						{
							Name: credsVolumeName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: cr.Spec.SecretsName,
								},
							},
						},
						{
							Name: tlsVolumeName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: spec.SSLSecretName,
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

func Service(cr *v2.PerconaServerForMySQL) *corev1.Service {
	labels := MatchLabels(cr)

	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceName(cr),
			Namespace: k8s.Namespace(cr),
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Ports: []corev1.ServicePort{
				{
					Name: "mysql",
					Port: DefaultPort,
				},
			},
			Selector:                 labels,
			PublishNotReadyAddresses: true,
		},
	}
}

func PrimaryService(cr *v2.PerconaServerForMySQL) *corev1.Service {
	labels := MatchLabels(cr)
	selector := util.SSMapCopy(labels)
	selector[v2.MySQLPrimaryLabel] = "true"

	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      PrimaryServiceName(cr),
			Namespace: k8s.Namespace(cr),
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name: "mysql",
					Port: DefaultPort,
				},
			},
			Selector: selector,
		},
	}
}

func mysqldContainer(cr *v2.PerconaServerForMySQL) corev1.Container {
	spec := cr.MySQLSpec()

	return corev1.Container{
		Name:            componentName,
		Image:           spec.Image,
		ImagePullPolicy: spec.ImagePullPolicy,
		Env: []corev1.EnvVar{
			{
				Name:  "SERVICE_NAME",
				Value: ServiceName(cr),
			},
			{
				Name:  "CLUSTER_HASH",
				Value: cr.ClusterHash(),
			},
		},
		Ports: []corev1.ContainerPort{
			{
				Name:          "mysql",
				ContainerPort: DefaultPort,
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      dataVolumeName,
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
		},
		Command:                  []string{"/var/lib/mysql/ps-entrypoint.sh"},
		Args:                     []string{"mysqld"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          spec.ContainerSecurityContext,
		StartupProbe: &corev1.Probe{
			Handler: corev1.Handler{
				Exec: &corev1.ExecAction{
					Command: []string{"/var/lib/mysql/bootstrap"},
				},
			},
			InitialDelaySeconds:           spec.StartupProbe.InitialDelaySeconds,
			TimeoutSeconds:                spec.StartupProbe.TimeoutSeconds,
			PeriodSeconds:                 spec.StartupProbe.PeriodSeconds,
			FailureThreshold:              spec.StartupProbe.FailureThreshold,
			SuccessThreshold:              spec.StartupProbe.SuccessThreshold,
			TerminationGracePeriodSeconds: spec.StartupProbe.TerminationGracePeriodSeconds,
		},
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt(DefaultPort),
				},
			},
			InitialDelaySeconds:           spec.ReadinessProbe.InitialDelaySeconds,
			TimeoutSeconds:                spec.ReadinessProbe.TimeoutSeconds,
			PeriodSeconds:                 spec.ReadinessProbe.PeriodSeconds,
			FailureThreshold:              spec.ReadinessProbe.FailureThreshold,
			SuccessThreshold:              spec.ReadinessProbe.SuccessThreshold,
			TerminationGracePeriodSeconds: spec.ReadinessProbe.TerminationGracePeriodSeconds,
		},
	}
}
