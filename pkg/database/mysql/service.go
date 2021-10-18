package mysql

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (m *MySQL) ServiceName() string {
	return m.Name()
}

func (m *MySQL) Service() *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.ServiceName(),
			Namespace: m.Namespace(),
			Labels:    m.MatchLabels(),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP:                "None",
			Ports:                    m.servicePorts(),
			Selector:                 m.MatchLabels(),
			PublishNotReadyAddresses: true,
		},
	}
}
