package orchestrator

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (o *Orchestrator) ServiceName() string {
	return o.Name()
}

func (o *Orchestrator) APIHost() string {
	return fmt.Sprintf("http://%s:%d", o.ServiceName(), DefaultWebPort)
}

func (o *Orchestrator) Service() *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.ServiceName(),
			Namespace: o.Namespace(),
			Labels:    o.MatchLabels(),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Ports: []corev1.ServicePort{
				{
					Name: "web",
					Port: int32(DefaultWebPort),
				},
				{
					Name: "raft",
					Port: int32(DefaultRaftPort),
				},
			},
			Selector:                 o.MatchLabels(),
			PublishNotReadyAddresses: true,
		},
	}
}
