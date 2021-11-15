package mysql

import (
	corev1 "k8s.io/api/core/v1"
)

func (m *MySQL) env() []corev1.EnvVar {
	return []corev1.EnvVar{
		{
			Name:  "MONITOR_HOST",
			Value: "%",
		},
		{
			Name:  "SERVICE_NAME",
			Value: m.ServiceName(),
		},
		{
			Name:  "SERVICE_NAME_UNREADY",
			Value: m.UnreadyServiceName(),
		},
		{
			Name:  "CLUSTER_HASH",
			Value: m.cluster.ClusterHash(),
		},
	}
}
