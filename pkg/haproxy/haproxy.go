package haproxy

import (
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/pmm"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

const (
	AppName         = "haproxy"
	credsVolumeName = "users"
	CredsMountPath  = "/etc/mysql/mysql-users-secret"
	tlsVolumeName   = "tls"
	tlsMountPath    = "/etc/mysql/mysql-tls-secret"
)

const (
	PortMySQL          = 3306
	PortMySQLReplicas  = 3307
	PortProxyProtocol  = 3309
	PortMySQLXProtocol = 33060
	PortAdmin          = 33062
	PortPMMStats       = 8404
)

func Name(cr *apiv1.PerconaServerMySQL) string {
	return cr.Name + "-" + AppName
}

func NamespacedName(cr *apiv1.PerconaServerMySQL) types.NamespacedName {
	return types.NamespacedName{Name: Name(cr), Namespace: cr.Namespace}
}

func ServiceName(cr *apiv1.PerconaServerMySQL) string {
	return Name(cr)
}

func Labels(cr *apiv1.PerconaServerMySQL) map[string]string {
	return util.SSMapMerge(cr.GlobalLabels(), cr.Spec.Proxy.HAProxy.Labels, MatchLabels(cr))
}

func MatchLabels(cr *apiv1.PerconaServerMySQL) map[string]string {
	return cr.Labels(AppName, naming.ComponentProxy)
}

func PodName(cr *apiv1.PerconaServerMySQL, idx int) string {
	return fmt.Sprintf("%s-%d", Name(cr), idx)
}

func Service(cr *apiv1.PerconaServerMySQL, secret *corev1.Secret) *corev1.Service {
	expose := cr.Spec.Proxy.HAProxy.Expose

	labels := MatchLabels(cr)
	labels = util.SSMapMerge(cr.GlobalLabels(), expose.Labels, labels)

	selector := MatchLabels(cr)

	serviceType := cr.Spec.Proxy.HAProxy.Expose.Type

	var loadBalancerSourceRanges []string
	if serviceType == corev1.ServiceTypeLoadBalancer {
		loadBalancerSourceRanges = expose.LoadBalancerSourceRanges
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
		{
			Name: "mysqlx",
			Port: int32(PortMySQLXProtocol),
		},
		{
			Name: "mysql-admin",
			Port: int32(PortAdmin),
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
			Annotations: util.SSMapMerge(cr.GlobalAnnotations(), expose.Annotations),
		},
		Spec: corev1.ServiceSpec{
			Type:                     serviceType,
			Ports:                    ports,
			Selector:                 selector,
			LoadBalancerSourceRanges: loadBalancerSourceRanges,
			InternalTrafficPolicy:    expose.InternalTrafficPolicy,
			ExternalTrafficPolicy:    externalTrafficPolicy,
		},
	}
}

func StatefulSet(cr *apiv1.PerconaServerMySQL, initImage, configHash, tlsHash string, secret *corev1.Secret) *appsv1.StatefulSet {
	selector := MatchLabels(cr)

	annotations := make(map[string]string)
	if configHash != "" {
		annotations[string(naming.AnnotationConfigHash)] = configHash
	}
	if tlsHash != "" {
		annotations[string(naming.AnnotationTLSHash)] = tlsHash
	}

	return &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        Name(cr),
			Namespace:   cr.Namespace,
			Labels:      Labels(cr),
			Annotations: cr.GlobalAnnotations(),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &cr.Spec.Proxy.HAProxy.Size,
			ServiceName: Name(cr),
			Selector: &metav1.LabelSelector{
				MatchLabels: selector,
			},
			UpdateStrategy: updateStrategy(cr),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      Labels(cr),
					Annotations: util.SSMapMerge(cr.GlobalAnnotations(), annotations),
				},
				Spec: cr.Spec.Proxy.HAProxy.Core(
					selector,
					volumes(cr),
					[]corev1.Container{
						k8s.InitContainer(
							cr,
							AppName,
							initImage,
							cr.Spec.Proxy.HAProxy.InitContainer,
							cr.Spec.Proxy.HAProxy.ImagePullPolicy,
							cr.Spec.Proxy.HAProxy.ContainerSecurityContext,
							cr.Spec.Proxy.HAProxy.Resources,
							nil,
						),
					},
					containers(cr, secret),
				),
			},
		},
	}
}

func volumes(cr *apiv1.PerconaServerMySQL) []corev1.Volume {
	t := true

	return []corev1.Volume{
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
										Path: "haproxy.cfg",
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

func updateStrategy(cr *apiv1.PerconaServerMySQL) appsv1.StatefulSetUpdateStrategy {
	switch cr.Spec.UpdateStrategy {
	case appsv1.OnDeleteStatefulSetStrategyType:
		return appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	default:
		var zero int32 = 0
		return appsv1.StatefulSetUpdateStrategy{
			Type: appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
				Partition: &zero,
			},
		}
	}
}

func containers(cr *apiv1.PerconaServerMySQL, secret *corev1.Secret) []corev1.Container {
	containers := []corev1.Container{
		haproxyContainer(cr),
		mysqlMonitContainer(cr),
	}

	if cr.PMMEnabled(secret) {
		pmmC := pmm.Container(
			cr,
			secret,
			AppName,
			"--listen-port="+strconv.Itoa(PortPMMStats))
		pmmC.Ports = append(pmmC.Ports, corev1.ContainerPort{ContainerPort: PortPMMStats})

		containers = append(containers, pmmC)
	}
	return containers
}

func haproxyContainer(cr *apiv1.PerconaServerMySQL) corev1.Container {
	spec := cr.Spec.Proxy.HAProxy

	env := []corev1.EnvVar{
		{
			Name:  "CLUSTER_TYPE",
			Value: string(cr.Spec.MySQL.ClusterType),
		},
	}
	env = append(env, spec.Env...)

	var readinessProbe, livenessProbe *corev1.Probe
	if cr.CompareVersion("0.12.0") >= 0 {
		readinessProbe = k8s.ExecProbe(spec.ReadinessProbe, []string{"/opt/percona/haproxy_readiness_check.sh"})
		livenessProbe = k8s.ExecProbe(spec.LivenessProbe, []string{"/opt/percona/haproxy_liveness_check.sh"})

		probsEnvs := []corev1.EnvVar{
			{
				Name:  "LIVENESS_CHECK_TIMEOUT",
				Value: fmt.Sprint(livenessProbe.TimeoutSeconds),
			},
			{
				Name:  "READINESS_CHECK_TIMEOUT",
				Value: fmt.Sprint(readinessProbe.TimeoutSeconds),
			},
		}
		env = append(env, probsEnvs...)
	}

	return corev1.Container{
		Name:            AppName,
		Image:           spec.Image,
		ImagePullPolicy: spec.ImagePullPolicy,
		Resources:       spec.Resources,
		Env:             env,
		EnvFrom:         spec.EnvFrom,
		Command:         []string{"/opt/percona/haproxy-entrypoint.sh"},
		Args:            []string{"haproxy"},
		ReadinessProbe:  readinessProbe,
		LivenessProbe:   livenessProbe,
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
			{
				Name:          "mysqlx",
				ContainerPort: int32(PortMySQLXProtocol),
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
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          spec.ContainerSecurityContext,
	}
}

func mysqlMonitContainer(cr *apiv1.PerconaServerMySQL) corev1.Container {
	spec := cr.Spec.Proxy.HAProxy

	env := []corev1.EnvVar{
		{
			Name:  "MYSQL_SERVICE",
			Value: mysql.ProxyServiceName(cr),
		},
	}
	env = append(env, spec.Env...)

	if cr.CompareVersion("0.12.0") >= 0 {
		cluserTypeEnv := []corev1.EnvVar{
			{
				Name:  "CLUSTER_TYPE",
				Value: string(cr.Spec.MySQL.ClusterType),
			},
		}
		env = append(env, cluserTypeEnv...)
	}

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
		Env:     env,
		EnvFrom: spec.EnvFrom,
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
				MountPath: CredsMountPath,
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
