package router

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

const (
	componentName   = "router"
	credsVolumeName = "users"
	credsMountPath  = "/etc/mysql/mysql-users-secret"
	tlsVolumeName   = "tls"
	tlsMountPath    = "/etc/mysql/mysql-tls-secret"
)

const (
	PortHTTP       = 8443
	PortRWDefault  = 3306
	PortReadWrite  = 6446
	PortReadOnly   = 6447
	PortXReadWrite = 6448
	PortXReadOnly  = 6449
	PortRWAdmin    = 33062
)

func Name(cr *apiv1alpha1.PerconaServerMySQL) string {
	return cr.Name + "-" + componentName
}

func ServiceName(cr *apiv1alpha1.PerconaServerMySQL) string {
	return Name(cr)
}

func MatchLabels(cr *apiv1alpha1.PerconaServerMySQL) map[string]string {
	return util.SSMapMerge(cr.MySQLSpec().Labels,
		map[string]string{apiv1alpha1.ComponentLabel: componentName},
		cr.Labels())
}

func Service(cr *apiv1alpha1.PerconaServerMySQL) *corev1.Service {
	labels := MatchLabels(cr)

	serviceType := cr.Spec.Router.Expose.Type

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
			Type: serviceType,
			Ports: []corev1.ServicePort{
				// do not change the port order
				// 8443 port should be the first in service, see K8SPS-132 task
				{
					Name: "http",
					Port: int32(PortHTTP),
				},
				{
					Name: "rw-default",
					Port: int32(PortRWDefault),
					TargetPort: intstr.IntOrString{
						IntVal: PortReadWrite,
					},
				},
				{
					Name: "read-write",
					Port: int32(PortReadWrite),
				},
				{
					Name: "read-only",
					Port: int32(PortReadOnly),
				},
				{
					Name: "x-read-write",
					Port: int32(PortXReadWrite),
				},
				{
					Name: "x-read-only",
					Port: int32(PortXReadOnly),
				},
				{
					Name: "rw-admin",
					Port: int32(PortRWAdmin),
				},
			},
			Selector: labels,
		},
	}
}

func Deployment(cr *apiv1alpha1.PerconaServerMySQL) *appsv1.Deployment {
	labels := MatchLabels(cr)
	spec := cr.Spec.Router
	replicas := spec.Size

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      Name(cr),
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					NodeSelector:     cr.Spec.Router.NodeSelector,
					Tolerations:      cr.Spec.Router.Tolerations,
					Containers:       containers(cr),
					Affinity:         spec.GetAffinity(labels),
					ImagePullSecrets: spec.ImagePullSecrets,
					// TerminationGracePeriodSeconds: 30,
					RestartPolicy:   corev1.RestartPolicyAlways,
					SchedulerName:   "default-scheduler",
					DNSPolicy:       corev1.DNSClusterFirst,
					SecurityContext: spec.PodSecurityContext,
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
				},
			},
		},
	}
}

func containers(cr *apiv1alpha1.PerconaServerMySQL) []corev1.Container {
	return []corev1.Container{routerContainer(cr)}
}

func routerContainer(cr *apiv1alpha1.PerconaServerMySQL) corev1.Container {
	spec := cr.Spec.Router

	return corev1.Container{
		Name:            componentName,
		Image:           spec.Image,
		ImagePullPolicy: spec.ImagePullPolicy,
		Resources:       spec.Resources,
		Env: []corev1.EnvVar{
			{
				Name:  "MYSQL_SERVICE_NAME",
				Value: mysql.ServiceName(cr),
			},
		},
		Ports: []corev1.ContainerPort{
			{
				Name:          "http",
				ContainerPort: int32(PortHTTP),
			},
			{
				Name:          "read-write",
				ContainerPort: int32(PortReadWrite),
			},
			{
				Name:          "read-only",
				ContainerPort: int32(PortReadOnly),
			},
			{
				Name:          "x-read-write",
				ContainerPort: int32(PortXReadWrite),
			},
			{
				Name:          "x-read-only",
				ContainerPort: int32(PortXReadOnly),
			},
			{
				Name:          "rw-admin",
				ContainerPort: int32(PortRWAdmin),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      credsVolumeName,
				MountPath: credsMountPath,
			},
			{
				Name:      tlsVolumeName,
				MountPath: tlsMountPath,
			},
		},
		Args:                     []string{"mysqlrouter", "-c", "/tmp/router/mysqlrouter.conf"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          spec.ContainerSecurityContext,
	}
}
