package orchestrator

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
)

func (o *Orchestrator) ServiceName(cr *v2.PerconaServerForMySQL) string {
	return o.Name
}

func (o *Orchestrator) Service(cr *v2.PerconaServerForMySQL) *corev1.Service {
	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.ServiceName(cr),
			Namespace: cr.Namespace,
			Labels:    o.MatchLabels(),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Ports:     o.servicePorts(),
			Selector:  o.MatchLabels(),
		},
	}
}
