package router

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

const (
	ComponentName    = "router"
	credsVolumeName  = "users"
	CredsMountPath   = "/etc/mysql/mysql-users-secret"
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
	PortXDefault   = 33060
	PortRWAdmin    = 33062
)

func Name(cr *apiv1alpha1.PerconaServerMySQL) string {
	return cr.Name + "-" + ComponentName
}

func PodName(cr *apiv1alpha1.PerconaServerMySQL, idx int) string {
	return fmt.Sprintf("%s-%d", Name(cr), idx)
}

func ServiceName(cr *apiv1alpha1.PerconaServerMySQL) string {
	return Name(cr)
}

func MatchLabels(cr *apiv1alpha1.PerconaServerMySQL) map[string]string {
	return util.SSMapMerge(cr.MySQLSpec().Labels,
		map[string]string{naming.LabelComponent: ComponentName},
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
					Name: "x-default",
					Port: int32(PortXDefault),
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

func Deployment(cr *apiv1alpha1.PerconaServerMySQL, initImage, configHash, tlsHash string) *appsv1.Deployment {
	labels := MatchLabels(cr)
	spec := cr.Spec.Proxy.Router
	replicas := spec.Size
	t := true

	annotations := make(map[string]string)
	if configHash != "" {
		annotations[string(naming.AnnotationConfigHash)] = configHash
	}
	if tlsHash != "" {
		annotations[string(naming.AnnotationTLSHash)] = tlsHash
	}

	zero := intstr.FromInt(0)
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
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxSurge: &zero,
				},
			},
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
					Containers:                    containers(cr),
					NodeSelector:                  cr.Spec.Proxy.Router.NodeSelector,
					Tolerations:                   cr.Spec.Proxy.Router.Tolerations,
					Affinity:                      spec.GetAffinity(labels),
					TopologySpreadConstraints:     spec.GetTopologySpreadConstraints(labels),
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
														Path: "mysqlrouter.conf",
													},
												},
												Optional: &t,
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

	env := []corev1.EnvVar{
		{
			Name:  "MYSQL_SERVICE_NAME",
			Value: mysql.ServiceName(cr),
		},
	}
	env = append(env, spec.Env...)

	return corev1.Container{
		Name:            ComponentName,
		Image:           spec.Image,
		ImagePullPolicy: spec.ImagePullPolicy,
		Resources:       spec.Resources,
		Env:             env,
		EnvFrom:         spec.EnvFrom,
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
				Name:          "x-default",
				ContainerPort: int32(PortXDefault),
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
		Command:                  []string{"/opt/percona/router-entrypoint.sh"},
		Args:                     []string{"mysqlrouter", "-c", "/tmp/router/mysqlrouter.conf"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          spec.ContainerSecurityContext,
		StartupProbe:             k8s.ExecProbe(spec.StartupProbe, []string{"/opt/percona/router_startup_check.sh"}),
		ReadinessProbe:           k8s.ExecProbe(spec.ReadinessProbe, []string{"/opt/percona/router_readiness_check.sh"}),
	}
}
