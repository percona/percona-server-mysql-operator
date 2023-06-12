package haproxy

import (
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/pmm"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

const (
	componentName   = "haproxy"
	credsVolumeName = "users"
	credsMountPath  = "/etc/mysql/mysql-users-secret"
	tlsVolumeName   = "tls"
	tlsMountPath    = "/etc/mysql/mysql-tls-secret"
)

const (
	PortMySQL         = 3306
	PortMySQLReplicas = 3307
	PortProxyProtocol = 3309
	PortPMMStats      = 8404
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

func Service(cr *apiv1alpha1.PerconaServerMySQL, secret *corev1.Secret) *corev1.Service {
	expose := cr.Spec.Proxy.HAProxy.Expose

	labels := MatchLabels(cr)
	labels = util.SSMapMerge(expose.Labels, labels)

	serviceType := cr.Spec.Proxy.HAProxy.Expose.Type

	var loadBalancerSourceRanges []string
	var loadBalancerIP string
	if serviceType == corev1.ServiceTypeLoadBalancer {
		loadBalancerSourceRanges = expose.LoadBalancerSourceRanges
		loadBalancerIP = expose.LoadBalancerIP
	}

	var externalTrafficPolicy corev1.ServiceExternalTrafficPolicyType
	if serviceType == corev1.ServiceTypeLoadBalancer || serviceType == corev1.ServiceTypeNodePort {
		externalTrafficPolicy = expose.ExternalTrafficPolicy
	}

	ports := []corev1.ServicePort{
		{
			Name: "mysql",
			Port: int32(PortMySQL),
		},
		{
			Name: "mysql-replicas",
			Port: int32(PortMySQLReplicas),
		},
		{
			Name: "proxy-protocol",
			Port: int32(PortProxyProtocol),
		},
	}

	if cr.PMMEnabled(secret) {
		ports = append(ports, corev1.ServicePort{
			Name: "pmm-stats",
			Port: int32(PortPMMStats),
		})
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
			Type:                     serviceType,
			Ports:                    ports,
			Selector:                 labels,
			LoadBalancerIP:           loadBalancerIP,
			LoadBalancerSourceRanges: loadBalancerSourceRanges,
			InternalTrafficPolicy:    expose.InternalTrafficPolicy,
			ExternalTrafficPolicy:    externalTrafficPolicy,
		},
	}
}

func StatefulSet(cr *apiv1alpha1.PerconaServerMySQL, initImage string, secret *corev1.Secret) *appsv1.StatefulSet {
	labels := MatchLabels(cr)

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
			Replicas:    &cr.Spec.Proxy.HAProxy.Size,
			ServiceName: Name(cr),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					NodeSelector: cr.Spec.Proxy.HAProxy.NodeSelector,
					Tolerations:  cr.Spec.Proxy.HAProxy.Tolerations,
					InitContainers: []corev1.Container{
						k8s.InitContainer(
							componentName,
							initImage,
							cr.Spec.Proxy.HAProxy.ImagePullPolicy,
							cr.Spec.Proxy.HAProxy.ContainerSecurityContext,
						),
					},
					Containers:       containers(cr, secret),
					Affinity:         cr.Spec.Proxy.HAProxy.GetAffinity(labels),
					ImagePullSecrets: cr.Spec.Proxy.HAProxy.ImagePullSecrets,
					// TerminationGracePeriodSeconds: 30,
					RestartPolicy: corev1.RestartPolicyAlways,
					SchedulerName: "default-scheduler",
					DNSPolicy:     corev1.DNSClusterFirst,
					Volumes: []corev1.Volume{
						{
							Name: "bin",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "haproxy-config",
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
					},
					SecurityContext: cr.Spec.Proxy.HAProxy.PodSecurityContext,
				},
			},
		},
	}
}

func containers(cr *apiv1alpha1.PerconaServerMySQL, secret *corev1.Secret) []corev1.Container {
	containers := []corev1.Container{
		haproxyContainer(cr),
		mysqlMonitContainer(cr),
	}
	if cr.PMMEnabled(secret) {
		pmmC := pmm.Container(cr, secret, componentName)

		pmmC.Env = append(pmmC.Env, corev1.EnvVar{
			Name:  "PMM_ADMIN_CUSTOM_PARAMS",
			Value: "--listen-port=" + strconv.Itoa(PortPMMStats),
		})
		pmmC.Ports = append(pmmC.Ports, corev1.ContainerPort{ContainerPort: PortPMMStats})

		containers = append(containers, pmmC)
	}
	return containers
}

func haproxyContainer(cr *apiv1alpha1.PerconaServerMySQL) corev1.Container {
	spec := cr.Spec.Proxy.HAProxy

	return corev1.Container{
		Name:            componentName,
		Image:           spec.Image,
		ImagePullPolicy: spec.ImagePullPolicy,
		Resources:       spec.Resources,
		Env:             []corev1.EnvVar{},
		Command:         []string{"/opt/percona/haproxy-entrypoint.sh"},
		Args:            []string{"haproxy"},
		Ports: []corev1.ContainerPort{
			{
				Name:          "mysql",
				ContainerPort: int32(PortMySQL),
			},
			{
				Name:          "mysql-replicas",
				ContainerPort: int32(PortMySQLReplicas),
			},
			{
				Name:          "proxy-protocol",
				ContainerPort: int32(PortProxyProtocol),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "bin",
				MountPath: "/opt/percona",
			},
			{
				Name:      "haproxy-config",
				MountPath: "/etc/haproxy/mysql",
			},
			{
				Name:      credsVolumeName,
				MountPath: credsMountPath,
			},
			{
				Name:      tlsVolumeName,
				MountPath: tlsMountPath,
			},
		},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          spec.ContainerSecurityContext,
	}
}

func mysqlMonitContainer(cr *apiv1alpha1.PerconaServerMySQL) corev1.Container {
	spec := cr.Spec.Proxy.HAProxy

	return corev1.Container{
		Name:            "mysql-monit",
		Image:           spec.Image,
		ImagePullPolicy: spec.ImagePullPolicy,
		Resources:       spec.Resources,
		Command:         []string{"/opt/percona/haproxy-entrypoint.sh"},
		Args: []string{
			"/opt/percona/peer-list",
			"-on-change=/opt/percona/haproxy_add_mysql_nodes.sh",
			"-service=$(MYSQL_SERVICE)",
		},
		Env: []corev1.EnvVar{
			{
				Name:  "MYSQL_SERVICE",
				Value: mysql.ServiceName(cr),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "bin",
				MountPath: "/opt/percona",
			},
			{
				Name:      "haproxy-config",
				MountPath: "/etc/haproxy/mysql",
			},
			{
				Name:      credsVolumeName,
				MountPath: credsMountPath,
			},
			{
				Name:      tlsVolumeName,
				MountPath: tlsMountPath,
			},
		},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          spec.ContainerSecurityContext,
	}
}
