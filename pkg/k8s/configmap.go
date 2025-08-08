package k8s

import (
	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ConfigMap(name, namespace, filename, data string, cr *apiv1alpha1.PerconaServerMySQL) *corev1.ConfigMap {

	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    naming.Labels(name, cr.Name, "percona-server", "database"),
		},
		Data: map[string]string{
			filename: data,
		},
	}
}
