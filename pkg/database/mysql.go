package database

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v2 "github.com/percona/percona-mysql/api/v2"
	"github.com/percona/percona-mysql/pkg/k8s"
)

const (
	Name           = "mysql"
	DataVolumeName = "datadir"
)

type MySQL struct {
	v2.MySQLSpec

	Name          string
	Namespace     string
	secretsName   string
	clusterLabels map[string]string
}

func New(cr *v2.PerconaServerForMySQL) *MySQL {
	return &MySQL{
		MySQLSpec:     cr.Spec.MySQL,
		Name:          cr.Name + "-" + Name,
		Namespace:     cr.Namespace,
		secretsName:   cr.Spec.SecretsName,
		clusterLabels: cr.Labels(),
	}
}

func (m *MySQL) MatchLabels() map[string]string {
	labels := m.clusterLabels
	for k, v := range m.Labels {
		if _, ok := labels[k]; !ok {
			labels[k] = v
		}
	}

	return labels
}

func (m *MySQL) StatefulSet() *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
			Labels:    m.MatchLabels(),
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: m.MatchLabels(),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: m.MatchLabels(),
				},
				Spec: corev1.PodSpec{
					InitContainers: m.InitContainers(),
					Containers:     m.Containers(),
					// TerminationGracePeriodSeconds: 30,
					RestartPolicy:   corev1.RestartPolicyAlways,
					SchedulerName:   "default-scheduler",
					DNSPolicy:       corev1.DNSClusterFirst,
					SecurityContext: m.PodSecurityContext,
				},
			},
		},
	}
}

func (m *MySQL) env() []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name:  "MONITOR_HOST",
			Value: "%",
		},
		{
			Name: "MONITOR_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(m.secretsName, v2.USERS_SECRET_KEY_MONITOR),
			},
		},
		{
			Name: "XTRABACKUP_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(m.secretsName, v2.USERS_SECRET_KEY_XTRABACKUP),
			},
		},
		{
			Name: "MYSQL_ROOT_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(m.secretsName, v2.USERS_SECRET_KEY_ROOT),
			},
		},
		{
			Name: "OPERATOR_ADMIN_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(m.secretsName, v2.USERS_SECRET_KEY_OPERATOR),
			},
		},
		{
			Name: "ORC_TOPOLOGY_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: k8s.SecretKeySelector(m.secretsName, v2.USERS_SECRET_KEY_ORCHESTRATOR),
			},
		},
		{
			Name:  "MY_NAMESPACE",
			Value: m.Namespace,
		},
		{
			Name:  "MY_SERVICE_NAME",
			Value: m.Name,
		},
		{
			Name:  "MY_FQDN",
			Value: "$(MY_POD_NAME).$(MY_SERVICE_NAME).$(MY_NAMESPACE)",
		},
	}
}

func (m *MySQL) ports() []corev1.ContainerPort {
	return []corev1.ContainerPort{
		{
			ContainerPort: 3306,
			Name:          "mysql",
		},
	}
}

func (m *MySQL) volumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      DataVolumeName,
			MountPath: "/var/lib/mysql",
		},
		{
			Name:      "config",
			MountPath: "/etc/mysql",
		},
	}
}

func (m *MySQL) Container() corev1.Container {
	return corev1.Container{
		Name:                     Name,
		Image:                    m.Image,
		ImagePullPolicy:          m.ImagePullPolicy,
		Env:                      m.env(),
		Ports:                    m.ports(),
		// VolumeMounts:             m.volumeMounts(),
		Command:                  []string{"/var/lib/mysql/ps-entrypoint.sh"},
		Args:                     []string{"mysqld"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
	}
}

func (m *MySQL) SidecarContainers() []corev1.Container {
	return nil
}

func (m *MySQL) Containers() []corev1.Container {
	containers := []corev1.Container{m.Container()}
	containers = append(containers, m.SidecarContainers()...)
	return containers
}

func (m *MySQL) InitContainers() []corev1.Container {
	return []corev1.Container{
		{
			Name:                     Name + "-init",
			Image:                    m.Image,
			ImagePullPolicy:          m.ImagePullPolicy,
			// VolumeMounts:             m.volumeMounts(),
			Command:                  []string{"/var/lib/mysql/ps-init-entrypoint.sh"},
			TerminationMessagePath:   "/dev/termination-log",
			TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		},
	}
}
