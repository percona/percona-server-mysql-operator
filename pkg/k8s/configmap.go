package k8s

import (
	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ConfigMap(cr *apiv1alpha1.PerconaServerMySQL, name, filename, data string) *corev1.ConfigMap {
	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   cr.Namespace,
			Labels:      cr.GlobalLabels(),
			Annotations: cr.GlobalAnnotations(),
		},
		Data: map[string]string{
			filename: data,
		},
	}
	if cr.CompareVersion("0.12.0") >= 0 {
		cm.Labels = naming.Labels(name, cr.Name, "percona-server", "database")
	}

	return cm
}
