package mysql

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v2 "github.com/percona/percona-mysql/api/v2"
)

func (m *MySQL) ServiceName(cr *v2.PerconaServerForMySQL) string {
	return cr.Name + "-" + Name
}

func (m *MySQL) Service(cr *v2.PerconaServerForMySQL) *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.ServiceName(cr),
			Namespace: cr.Namespace,
			Labels:    m.MatchLabels(),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Ports:     m.servicePorts(),
			Selector:  m.MatchLabels(),
		},
	}
}
