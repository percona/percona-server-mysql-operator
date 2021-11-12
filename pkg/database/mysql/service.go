package mysql

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
)

func (m *MySQL) ServiceName() string {
	return m.Name()
}

func (m *MySQL) PrimaryServiceName() string {
	return m.Name() + "-primary"
}

func (m *MySQL) UnreadyServiceName() string {
	return m.Name() + "-unready"
}

func (m *MySQL) UnreadyService() *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.UnreadyServiceName(),
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
			ClusterIP: "None",
			Ports:     m.servicePorts(),
			Selector:  m.MatchLabels(),
		},
	}
}

func (m *MySQL) PrimaryService() *corev1.Service {
	selector := m.MatchLabels()
	selector[v2.MySQLPrimaryLabel] = "true"

	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.PrimaryServiceName(),
			Namespace: m.Namespace(),
			Labels:    m.MatchLabels(),
		},
		Spec: corev1.ServiceSpec{
			Ports:    m.servicePorts(),
			Selector: selector,
		},
	}
}
