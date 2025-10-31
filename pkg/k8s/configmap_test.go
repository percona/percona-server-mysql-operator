package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
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
		assert.Equal(t, name, cfg.Name)
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

func TestEqualConfigMaps(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		result := EqualConfigMaps()
		assert.False(t, result)
	})

	t.Run("single configmap", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "test-ns",
			},
			Data: map[string]string{
				"key": "value",
			},
		}
		result := EqualConfigMaps(cm)
		assert.False(t, result)
	})

	t.Run("single nil configmap", func(t *testing.T) {
		result := EqualConfigMaps(nil)
		assert.False(t, result)
	})

	t.Run("two identical configmaps", func(t *testing.T) {
		cm1 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "test-ns",
				Labels: map[string]string{
					"app": "myapp",
				},
			},
			Data: map[string]string{
				"key": "value",
			},
		}
		cm2 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "test-ns",
				Labels: map[string]string{
					"app": "myapp",
				},
			},
			Data: map[string]string{
				"key": "value",
			},
		}
		result := EqualConfigMaps(cm1, cm2)
		assert.True(t, result)
	})

	t.Run("two configmaps with different data", func(t *testing.T) {
		cm1 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "test-ns",
			},
			Data: map[string]string{
				"key": "value1",
			},
		}
		cm2 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "test-ns",
			},
			Data: map[string]string{
				"key": "value2",
			},
		}
		result := EqualConfigMaps(cm1, cm2)
		assert.False(t, result)
	})

	t.Run("two configmaps with different labels", func(t *testing.T) {
		cm1 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "test-ns",
				Labels: map[string]string{
					"app": "app1",
				},
			},
			Data: map[string]string{
				"key": "value",
			},
		}
		cm2 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "test-ns",
				Labels: map[string]string{
					"app": "app2",
				},
			},
			Data: map[string]string{
				"key": "value",
			},
		}
		result := EqualConfigMaps(cm1, cm2)
		assert.False(t, result)
	})

	t.Run("two nil configmaps", func(t *testing.T) {
		result := EqualConfigMaps(nil, nil)
		assert.True(t, result)
	})

	t.Run("first configmap nil, second not nil", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "test-ns",
			},
			Data: map[string]string{
				"key": "value",
			},
		}
		result := EqualConfigMaps(nil, cm)
		assert.False(t, result)
	})

	t.Run("first configmap not nil, second nil", func(t *testing.T) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "test-ns",
			},
			Data: map[string]string{
				"key": "value",
			},
		}
		result := EqualConfigMaps(cm, nil)
		assert.False(t, result)
	})

	t.Run("multiple configmaps all equal", func(t *testing.T) {
		cm1 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "test-ns",
				Labels: map[string]string{
					"app": "myapp",
				},
			},
			Data: map[string]string{
				"key": "value",
			},
		}
		cm2 := cm1.DeepCopy()
		cm3 := cm1.DeepCopy()
		result := EqualConfigMaps(cm1, cm2, cm3)
		assert.True(t, result)
	})

	t.Run("multiple configmaps with one different", func(t *testing.T) {
		cm1 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "test-ns",
			},
			Data: map[string]string{
				"key": "value",
			},
		}
		cm2 := cm1.DeepCopy()
		cm3 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "test-ns",
			},
			Data: map[string]string{
				"key": "different-value",
			},
		}
		result := EqualConfigMaps(cm1, cm2, cm3)
		assert.False(t, result)
	})

	t.Run("multiple configmaps with nil in the middle", func(t *testing.T) {
		cm1 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-cm",
				Namespace: "test-ns",
			},
			Data: map[string]string{
				"key": "value",
			},
		}
		cm2 := cm1.DeepCopy()
		result := EqualConfigMaps(cm1, nil, cm2)
		assert.False(t, result)
	})

	t.Run("multiple nil configmaps", func(t *testing.T) {
		result := EqualConfigMaps(nil, nil, nil)
		assert.True(t, result)
	})
}
