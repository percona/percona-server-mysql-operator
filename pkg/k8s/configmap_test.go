package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
)

func TestConfigMap(t *testing.T) {
	cluster := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster1",
			Namespace: "configmap-ns",
		},
		Spec: apiv1.PerconaServerMySQLSpec{
			CRVersion: version.Version(),
		},
	}

	const name = "configmap-name"
	const filename = "some-file"
	const data = "some-data"
	const component = "some-component"

	t.Run("basic", func(t *testing.T) {
		cr := cluster.DeepCopy()

		cfg := ConfigMap(cr, name, filename, data, component)
		assert.Equal(t, name, cfg.Name, name)
		assert.Equal(t, cluster.Namespace, cfg.Namespace)
		assert.Equal(t, map[string]string{
			"app.kubernetes.io/component":  "some-component",
			"app.kubernetes.io/instance":   "cluster1",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/name":       "configmap-name",
			"app.kubernetes.io/part-of":    "percona-server",
		}, cfg.Labels)
		assert.Equal(t, map[string]string(nil), cfg.Annotations)
		assert.Equal(t, map[string]string{
			filename: data,
		}, cfg.Data)
	})

	t.Run("metadata labels and annotations", func(t *testing.T) {
		cr := cluster.DeepCopy()
		cr.Spec.Metadata = &apiv1.Metadata{
			Labels: map[string]string{
				"custom-label": "custom-value",
			},
			Annotations: map[string]string{
				"custom-annotation": "custom-value",
			},
		}

		cfg := ConfigMap(cr, name, filename, data, component)
		assert.Equal(t, map[string]string{
			"app.kubernetes.io/component":  "some-component",
			"app.kubernetes.io/instance":   "cluster1",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/name":       "configmap-name",
			"app.kubernetes.io/part-of":    "percona-server",
			"custom-label":                 "custom-value",
		}, cfg.Labels)
		assert.Equal(t, map[string]string{
			"custom-annotation": "custom-value",
		}, cfg.Annotations)
	})

	t.Run("metadata labels should not take precedence", func(t *testing.T) {
		cr := cluster.DeepCopy()
		cr.Spec.Metadata = &apiv1.Metadata{
			Labels: map[string]string{
				"app.kubernetes.io/component":  "invalid-component",
				"app.kubernetes.io/instance":   "invalid-instance",
				"app.kubernetes.io/managed-by": "invalid-operator",
				"app.kubernetes.io/name":       "invalid-name",
				"app.kubernetes.io/part-of":    "invalid-part",
			},
		}

		cfg := ConfigMap(cr, name, filename, data, component)
		assert.Equal(t, map[string]string{
			"app.kubernetes.io/component":  "some-component",
			"app.kubernetes.io/instance":   "cluster1",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/name":       "configmap-name",
			"app.kubernetes.io/part-of":    "percona-server",
		}, cfg.Labels)
	})
}
