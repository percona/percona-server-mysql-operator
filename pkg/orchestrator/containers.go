package orchestrator

import (
	v2 "github.com/percona/percona-server-mysql-operator/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func containers(cr *v2.PerconaServerForMySQL) []corev1.Container {
	sidecars := sidecarContainers(cr)
	containers := make([]corev1.Container, 1, len(sidecars)+1)
	containers[0] = container(cr)
	return append(containers, sidecars...)
}

func container(cr *v2.PerconaServerForMySQL) corev1.Container {
	serviceName := mysql.ServiceName(cr)

	return corev1.Container{
		Name:            componentName,
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
		Ports:                    containerPorts(),
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

func sidecarContainers(cr *v2.PerconaServerForMySQL) []corev1.Container {
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
