package k8s

import (
	"reflect"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

func ConfigMap(cr *apiv1.PerconaServerMySQL, name, filename, data string, component string) *corev1.ConfigMap {
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
		if cm.Labels == nil {
			cm.Labels = map[string]string{}
		}

		cm.Labels = util.SSMapMerge(naming.Labels(name, cr.Name, "percona-server", component), cm.Labels)

		if cr.CompareVersion("1.0.0") >= 0 {
			cm.Labels = util.SSMapMerge(cm.Labels, naming.Labels(name, cr.Name, "percona-server", component))
		}
	}

	return cm
}

func EqualConfigMaps(cfgs ...*corev1.ConfigMap) bool {
	if len(cfgs) == 0 {
		return false
	}
	if len(cfgs) == 1 {
		return true
	}

	configMap := cfgs[0]

	for i := 1; i < len(cfgs); i++ {
		if configMap == nil && cfgs[i] == nil {
			continue
		}
		if configMap == nil || cfgs[i] == nil {
			return false
		}
		if !reflect.DeepEqual(configMap.Data, cfgs[i].Data) || !EqualMetadata(configMap.ObjectMeta, cfgs[i].ObjectMeta) {
			return false
		}
	}
	return true
}
