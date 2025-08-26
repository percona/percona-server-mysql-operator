package router

import (
	"fmt"
	"slices"

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
	AppName          = "router"
	credsVolumeName  = "users"
	CredsMountPath   = "/etc/mysql/mysql-users-secret"
	tlsVolumeName    = "tls"
	tlsMountPath     = "/etc/mysql/mysql-tls-secret"
	configVolumeName = "config"
	configMountPath  = "/etc/mysql/config"
	CustomConfigKey  = "mysqlrouter.conf"
)

const (
	portHTTP       = 8443
	portRWDefault  = 3306
	portReadWrite  = 6446
	portReadOnly   = 6447
	portXReadWrite = 6448
	portXReadOnly  = 6449
	portXDefault   = 33060
	portRWAdmin    = 33062
)

func Name(cr *apiv1alpha1.PerconaServerMySQL) string {
	return cr.Name + "-" + AppName
}

func PodName(cr *apiv1alpha1.PerconaServerMySQL, idx int) string {
	return fmt.Sprintf("%s-%d", Name(cr), idx)
}

func ServiceName(cr *apiv1alpha1.PerconaServerMySQL) string {
	return Name(cr)
}

func MatchLabels(cr *apiv1alpha1.PerconaServerMySQL) map[string]string {
	return util.SSMapMerge(cr.MySQLSpec().Labels,
		cr.Labels(AppName, naming.ComponentProxy))
}

func Service(cr *apiv1alpha1.PerconaServerMySQL) *corev1.Service {
	expose := cr.Spec.Proxy.Router.Expose

	labels := util.SSMapMerge(expose.Labels, MatchLabels(cr))

	var loadBalancerSourceRanges []string
	if expose.Type == corev1.ServiceTypeLoadBalancer {
		loadBalancerSourceRanges = expose.LoadBalancerSourceRanges
	}

	var externalTrafficPolicy corev1.ServiceExternalTrafficPolicyType
	if expose.Type == corev1.ServiceTypeLoadBalancer || expose.Type == corev1.ServiceTypeNodePort {
		externalTrafficPolicy = expose.ExternalTrafficPolicy
	}

	s := &corev1.Service{
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
			Type:                     expose.Type,
			Ports:                    ports(cr.Spec.Proxy.Router.Ports),
			Selector:                 labels,
			LoadBalancerSourceRanges: loadBalancerSourceRanges,
			InternalTrafficPolicy:    expose.InternalTrafficPolicy,
			ExternalTrafficPolicy:    externalTrafficPolicy,
		},
	}

	return s
}

func Deployment(cr *apiv1alpha1.PerconaServerMySQL, initImage, configHash, tlsHash string) *appsv1.Deployment {
	labels := MatchLabels(cr)
	spec := cr.Spec.Proxy.Router
	replicas := spec.Size

	annotations := make(map[string]string)
	if configHash != "" {
		annotations[string(naming.AnnotationConfigHash)] = configHash
	}
	if tlsHash != "" {
		annotations[string(naming.AnnotationTLSHash)] = tlsHash
	}

	zero := intstr.FromInt32(0)
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
					Containers:                    containers(cr),
					NodeSelector:                  cr.Spec.Proxy.Router.NodeSelector,
					Tolerations:                   cr.Spec.Proxy.Router.Tolerations,
					Affinity:                      spec.GetAffinity(labels),
					TopologySpreadConstraints:     spec.GetTopologySpreadConstraints(labels),
					ImagePullSecrets:              spec.ImagePullSecrets,
					TerminationGracePeriodSeconds: spec.GetTerminationGracePeriodSeconds(),
					RestartPolicy:                 corev1.RestartPolicyAlways,
					SchedulerName:                 spec.SchedulerName,
					RuntimeClassName:              spec.RuntimeClassName,
					ServiceAccountName:            spec.ServiceAccountName,
					DNSPolicy:                     corev1.DNSClusterFirst,
					SecurityContext:               spec.PodSecurityContext,
					Volumes:                       volumes(cr),
				},
			},
		},
	}
}

func volumes(cr *apiv1alpha1.PerconaServerMySQL) []corev1.Volume {
	t := true

	return []corev1.Volume{
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
	}
}

func containers(cr *apiv1alpha1.PerconaServerMySQL) []corev1.Container {
	return []corev1.Container{routerContainer(cr)}
}

func ports(sPorts []corev1.ServicePort) []corev1.ServicePort {
	defaultPorts := []corev1.ServicePort{
		// do not change the port order
		// 8443 port should be the first in service, see K8SPS-132 task
		{
			Name: "http",
			Port: int32(portHTTP),
		},
		{
			Name: "rw-default",
			Port: int32(portRWDefault),
			TargetPort: intstr.IntOrString{
				IntVal: portReadWrite,
			},
		},
		{
			Name: "read-write",
			Port: int32(portReadWrite),
		},
		{
			Name: "read-only",
			Port: int32(portReadOnly),
		},
		{
			Name: "x-read-write",
			Port: int32(portXReadWrite),
		},
		{
			Name: "x-read-only",
			Port: int32(portXReadOnly),
		},
		{
			Name: "x-default",
			Port: int32(portXDefault),
		},
		{
			Name: "rw-admin",
			Port: int32(portRWAdmin),
		},
	}
	if len(sPorts) == 0 {
		return defaultPorts
	}

	specifiedPorts := make([]corev1.ServicePort, len(sPorts))
	copy(specifiedPorts, sPorts)

	ports := []corev1.ServicePort{}
	for _, defaultPort := range defaultPorts {
		idx := slices.IndexFunc(specifiedPorts, func(port corev1.ServicePort) bool {
			return port.Name == defaultPort.Name
		})
		if idx == -1 {
			ports = append(ports, defaultPort)
			continue
		}

		modifiedPort := specifiedPorts[idx]
		if modifiedPort.Port == 0 {
			modifiedPort.Port = defaultPort.Port
		}
		if modifiedPort.TargetPort.IntValue() == 0 {
			modifiedPort.TargetPort = defaultPort.TargetPort
		}
		ports = append(ports, modifiedPort)
		specifiedPorts = slices.Delete(specifiedPorts, idx, idx+1)
	}

	ports = append(ports, specifiedPorts...)

	return ports
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

	c := corev1.Container{
		Name:            AppName,
		Image:           spec.Image,
		ImagePullPolicy: spec.ImagePullPolicy,
		Resources:       spec.Resources,
		Env:             env,
		EnvFrom:         spec.EnvFrom,
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

	for _, servicePort := range ports(cr.Spec.Proxy.Router.Ports) {
		containerPort := servicePort.Port
		if targetPort := servicePort.TargetPort.IntValue(); targetPort != 0 {
			containerPort = int32(targetPort)
		}

		c.Ports = append(c.Ports, corev1.ContainerPort{
			Name:          servicePort.Name,
			ContainerPort: containerPort,
			Protocol:      servicePort.Protocol,
		})
	}

	return c
}
