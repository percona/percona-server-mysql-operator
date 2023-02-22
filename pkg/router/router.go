package router

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

const (
	componentName    = "router"
	credsVolumeName  = "users"
	credsMountPath   = "/etc/mysql/mysql-users-secret"
	tlsVolumeName    = "tls"
	tlsMountPath     = "/etc/mysql/mysql-tls-secret"
	configVolumeName = "config"
	configMountPath  = "/etc/mysql/config"
	CustomConfigKey  = "mysqlrouter.conf"
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
	expose := cr.Spec.Proxy.Router.Expose

	labels := util.SSMapMerge(expose.Labels, MatchLabels(cr))

	var loadBalancerSourceRanges []string
	var loadBalancerIP string
	if expose.Type == corev1.ServiceTypeLoadBalancer {
		loadBalancerSourceRanges = expose.LoadBalancerSourceRanges
		loadBalancerIP = expose.LoadBalancerIP
	}

	var externalTrafficPolicy corev1.ServiceExternalTrafficPolicyType
	if expose.Type == corev1.ServiceTypeLoadBalancer || expose.Type == corev1.ServiceTypeNodePort {
		externalTrafficPolicy = expose.ExternalTrafficPolicy
	}

	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        ServiceName(cr),
			Namespace:   cr.Namespace,
			Labels:      labels,
			Annotations: expose.Annotations,
		},
		Spec: corev1.ServiceSpec{
			Type: expose.Type,
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
			Selector:                 labels,
			LoadBalancerIP:           loadBalancerIP,
			LoadBalancerSourceRanges: loadBalancerSourceRanges,
			InternalTrafficPolicy:    expose.InternalTrafficPolicy,
			ExternalTrafficPolicy:    externalTrafficPolicy,
		},
	}
}

func Deployment(cr *apiv1alpha1.PerconaServerMySQL, initImage, configHash string) *appsv1.Deployment {
	labels := MatchLabels(cr)
	spec := cr.Spec.Proxy.Router
	replicas := spec.Size

	annotations := make(map[string]string)
	annotations["percona.com/configuration-hash"] = configHash // TODO: set this only if there is a hash

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
					InitContainers: []corev1.Container{
						k8s.InitContainer(
							componentName,
							initImage,
							spec.ImagePullPolicy,
							spec.ContainerSecurityContext,
						),
					},
					Containers:                    containers(cr),
					NodeSelector:                  cr.Spec.Proxy.Router.NodeSelector,
					Tolerations:                   cr.Spec.Proxy.Router.Tolerations,
					Affinity:                      spec.GetAffinity(labels),
					ImagePullSecrets:              spec.ImagePullSecrets,
					TerminationGracePeriodSeconds: spec.TerminationGracePeriodSeconds,
					RestartPolicy:                 corev1.RestartPolicyAlways,
					SchedulerName:                 spec.SchedulerName,
					RuntimeClassName:              spec.RuntimeClassName,
					ServiceAccountName:            spec.ServiceAccountName,
					DNSPolicy:                     corev1.DNSClusterFirst,
					SecurityContext:               spec.PodSecurityContext,
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
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ConfigMap: &corev1.ConfigMapProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: Name(cr),
												},
												Items: []corev1.KeyToPath{
													{
														Key:  CustomConfigKey,
														Path: "aaaaa/mysqlrouter.conf",
													},
												},
												Optional: new(bool),
											},
										},
									},
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
	spec := cr.Spec.Proxy.Router

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
				Name:      apiv1alpha1.BinVolumeName,
				MountPath: apiv1alpha1.BinVolumePath,
			},
			{
				Name:      credsVolumeName,
				MountPath: credsMountPath,
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
		Command:                  []string{"/opt/percona/router-entrypoint.sh"},
		Args:                     []string{"mysqlrouter", "-c", "/tmp/router/mysqlrouter.conf"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          spec.ContainerSecurityContext,
		ReadinessProbe:           k8s.ExecProbe(spec.ReadinessProbe, []string{"/opt/percona/router_readiness_check.sh"}),
	}
}
