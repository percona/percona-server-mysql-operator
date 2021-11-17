package orchestrator

import (
	"fmt"
	"path/filepath"

	apiv2 "github.com/percona/percona-server-mysql-operator/api/v2"
	v2 "github.com/percona/percona-server-mysql-operator/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
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
func Name(cr *apiv2.PerconaServerForMySQL) string {
	return cr.Name + "-" + componentName
}

func ServiceName(cr *apiv2.PerconaServerForMySQL) string {
	return Name(cr)
}

func APIHost(serviceName string) string {
	return fmt.Sprintf("http://%s:%d", serviceName, defaultWebPort)
}

func podSpec(cr *apiv2.PerconaServerForMySQL) *v2.PodSpec {
	return &cr.Spec.Orchestrator
}

// Labels returns labels of orchestrator
func Labels(cr *apiv2.PerconaServerForMySQL) map[string]string {
	return podSpec(cr).Labels
}

func MatchLabels(cr *apiv2.PerconaServerForMySQL) map[string]string {
	return util.SSMapMerge(Labels(cr),
		map[string]string{apiv2.ComponentLabel: componentName},
		cr.Labels())
}

func StatefulSet(cr *apiv2.PerconaServerForMySQL) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      Name(cr),
			Namespace: k8s.Namespace(cr),
			Labels:    MatchLabels(cr),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &cr.Spec.Orchestrator.Size,
			ServiceName: Name(cr),
			Selector: &metav1.LabelSelector{
				MatchLabels: MatchLabels(cr),
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				k8s.PVC(dataVolumeName, cr.Spec.Orchestrator.VolumeSpec),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: MatchLabels(cr),
				},
				Spec: corev1.PodSpec{
					Containers: containers(cr),
					// TerminationGracePeriodSeconds: 30,
					RestartPolicy: corev1.RestartPolicyAlways,
					SchedulerName: "default-scheduler",
					DNSPolicy:     corev1.DNSClusterFirst,
					Volumes: []corev1.Volume{
						{
							Name: credsVolumeName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: k8s.SecretsName(cr),
								},
							},
						},
						{
							Name: tlsVolumeName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: k8s.SSLSecretName(cr),
								},
							},
						},
					},
					SecurityContext: cr.Spec.Orchestrator.PodSecurityContext,
				},
			},
		},
	}
}

func containers(cr *apiv2.PerconaServerForMySQL) []corev1.Container {
	sidecars := sidecarContainers(cr)
	containers := make([]corev1.Container, 1, len(sidecars)+1)
	containers[0] = container(cr)
	return append(containers, sidecars...)
}

func container(cr *apiv2.PerconaServerForMySQL) corev1.Container {
	return corev1.Container{
		Name:            componentName,
		Image:           cr.Spec.Orchestrator.Image,
		ImagePullPolicy: cr.Spec.Orchestrator.ImagePullPolicy,
		Env: []corev1.EnvVar{
			{
				Name:  "ORC_SERVICE",
				Value: ServiceName(cr),
			},
			{
				Name:  "MYSQL_SERVICE",
				Value: mysql.ServiceName(cr),
			},
		},
		Ports: []corev1.ContainerPort{
			{
				Name:          "web",
				ContainerPort: int32(defaultWebPort),
			},
			{
				Name:          "raft",
				ContainerPort: int32(defaultRaftPort),
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
			InitialDelaySeconds: int32(10),
			TimeoutSeconds:      int32(3),
			PeriodSeconds:       int32(5),
			FailureThreshold:    int32(3),
			SuccessThreshold:    int32(1),
		},
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: "/api/health",
					Port: intstr.FromString("web"),
				},
			},
			InitialDelaySeconds: int32(30),
			TimeoutSeconds:      int32(3),
			PeriodSeconds:       int32(5),
			FailureThreshold:    int32(3),
			SuccessThreshold:    int32(1),
		},
	}
}

func sidecarContainers(cr *apiv2.PerconaServerForMySQL) []corev1.Container {
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
			MountPath: filepath.Join(CredsMountPath, apiv2.USERS_SECRET_KEY_ORCHESTRATOR),
			SubPath:   apiv2.USERS_SECRET_KEY_ORCHESTRATOR,
		},
	}
}

func Service(cr *apiv2.PerconaServerForMySQL) *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceName(cr),
			Namespace: k8s.Namespace(cr),
			Labels:    MatchLabels(cr),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Ports: []corev1.ServicePort{
				{
					Name: "web",
					Port: int32(defaultWebPort),
				},
				{
					Name: "raft",
					Port: int32(defaultRaftPort),
				},
			},
			Selector:                 MatchLabels(cr),
			PublishNotReadyAddresses: true,
		},
	}
}
