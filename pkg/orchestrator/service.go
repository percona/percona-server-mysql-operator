package orchestrator

import (
	"fmt"

	v2 "github.com/percona/percona-server-mysql-operator/api/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ServiceName(cr *v2.PerconaServerForMySQL) string {
	return name(cr)
}

func APIHost(cr *v2.PerconaServerForMySQL) string {
	return fmt.Sprintf("http://%s:%d", ServiceName(cr), defaultWebPort)
}

func Service(cr *v2.PerconaServerForMySQL) *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceName(cr),
			Namespace: namespace(cr),
			Labels:    matchLabels(cr),
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
			Selector:                 matchLabels(cr),
			PublishNotReadyAddresses: true,
		},
	}
}
