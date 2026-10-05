package logcollector

import (
	"context"
	goerrors "errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/logcollector/logrotate"
)

const testNamespace = "ns"

func buildFakeClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func getConfigMap(ctx context.Context, cl client.Client, name string) (*corev1.ConfigMap, error) {
	cm := new(corev1.ConfigMap)
	err := cl.Get(ctx, types.NamespacedName{Name: name, Namespace: testNamespace}, cm)
	return cm, err
}

func ownedConfigMap(t *testing.T, cr *apiv1.PerconaServerMySQL, name string, data map[string]string) *corev1.ConfigMap {
	t.Helper()

	return &corev1.ConfigMap{
		Name:      name,
		Namespace: testNamespace,
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: apiv1.GroupVersion.String(),
			Kind:       "PerconaServerMySQL",
			Name:       cr.Name,
			UID:        cr.UID,
			Controller: new(true),
		}},
		Data: data,
	}
}

func TestReconcileSpecAbsent(t *testing.T) {
	cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
		cr.Spec.LogCollector = nil
	})
	cl := buildFakeClient(t, cr)

	require.NoError(t, Reconcile(t.Context(), cl, cr))
	assert.Nil(t, cr.Spec.LogCollector)
}

func TestReconcileSkipsOldCRVersion(t *testing.T) {
	cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
		cr.Spec.CRVersion = testOldCRVersion
		cr.Spec.LogCollector.Enabled = nil
		cr.Spec.LogCollector.Configuration = "pipeline: {}"
	})
	cl := buildFakeClient(t, cr)

	require.NoError(t, Reconcile(t.Context(), cl, cr))

	assert.Nil(t, cr.Spec.LogCollector.Enabled, "enabled must not be defaulted below 1.3.0")

	_, err := getConfigMap(t.Context(), cl, ConfigMapName(testClusterName))
	assert.True(t, k8serrors.IsNotFound(err), "no ConfigMap should be created below 1.3.0")
}

func TestReconcileFluentBitConfigMap(t *testing.T) {
	t.Run("creates the config map", func(t *testing.T) {
		cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.Configuration = "pipeline: {}"
		})
		cl := buildFakeClient(t, cr)

		require.NoError(t, Reconcile(t.Context(), cl, cr))

		cm, err := getConfigMap(t.Context(), cl, ConfigMapName(testClusterName))
		require.NoError(t, err)
		assert.Equal(t, "pipeline: {}", cm.Data[fluentBitCustomConfigurationFile])
		assert.True(t, metav1.IsControlledBy(cm, cr), "config map must be owned by the cluster")
	})

	t.Run("updates the config map on change", func(t *testing.T) {
		cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.Configuration = "pipeline: {}"
		})
		existing := ownedConfigMap(t, cr, ConfigMapName(testClusterName), map[string]string{
			fluentBitCustomConfigurationFile: "old",
		})
		cl := buildFakeClient(t, cr, existing)

		require.NoError(t, Reconcile(t.Context(), cl, cr))

		cm, err := getConfigMap(t.Context(), cl, ConfigMapName(testClusterName))
		require.NoError(t, err)
		assert.Equal(t, "pipeline: {}", cm.Data[fluentBitCustomConfigurationFile])
	})

	t.Run("deletes an owned config map when configuration is cleared", func(t *testing.T) {
		cr := testCR()
		existing := ownedConfigMap(t, cr, ConfigMapName(testClusterName), map[string]string{
			fluentBitCustomConfigurationFile: "old",
		})
		cl := buildFakeClient(t, cr, existing)

		require.NoError(t, Reconcile(t.Context(), cl, cr))

		_, err := getConfigMap(t.Context(), cl, ConfigMapName(testClusterName))
		assert.True(t, k8serrors.IsNotFound(err))
	})

	t.Run("deletes an owned config map when the collector is disabled", func(t *testing.T) {
		cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.Enabled = new(false)
			cr.Spec.LogCollector.Configuration = "pipeline: {}"
		})
		existing := ownedConfigMap(t, cr, ConfigMapName(testClusterName), map[string]string{
			fluentBitCustomConfigurationFile: "old",
		})
		cl := buildFakeClient(t, cr, existing)

		require.NoError(t, Reconcile(t.Context(), cl, cr))

		_, err := getConfigMap(t.Context(), cl, ConfigMapName(testClusterName))
		assert.True(t, k8serrors.IsNotFound(err))
	})

	t.Run("leaves a config map the operator does not own", func(t *testing.T) {
		cr := testCR()
		foreign := &corev1.ConfigMap{
			Name:      ConfigMapName(testClusterName),
			Namespace: testNamespace,
			Data:      map[string]string{"keep": "me"},
		}
		cl := buildFakeClient(t, cr, foreign)

		require.NoError(t, Reconcile(t.Context(), cl, cr))

		cm, err := getConfigMap(t.Context(), cl, ConfigMapName(testClusterName))
		require.NoError(t, err)
		assert.Equal(t, "me", cm.Data["keep"])
	})

	t.Run("refuses to take over a config map the operator does not own", func(t *testing.T) {
		cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.Configuration = "pipeline: {}"
		})
		foreign := &corev1.ConfigMap{
			Name:      ConfigMapName(testClusterName),
			Namespace: testNamespace,
			Data:      map[string]string{"keep": "me"},
		}
		cl := buildFakeClient(t, cr, foreign)

		err := Reconcile(t.Context(), cl, cr)

		require.EqualError(t, err, "fluent-bit config map: ConfigMap/"+
			ConfigMapName(testClusterName)+" already exists and is not controlled by this cluster")

		cm, err := getConfigMap(t.Context(), cl, ConfigMapName(testClusterName))
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"keep": "me"}, cm.Data)
		assert.Empty(t, cm.OwnerReferences)
	})
}

func TestReconcileLogRotateConfigMap(t *testing.T) {
	name := logrotate.ConfigMapName(testClusterName)

	t.Run("creates the config map", func(t *testing.T) {
		cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{Configuration: "/x {}"}
		})
		cl := buildFakeClient(t, cr)

		require.NoError(t, Reconcile(t.Context(), cl, cr))

		cm, err := getConfigMap(t.Context(), cl, name)
		require.NoError(t, err)
		assert.Equal(t, "/x {}", cm.Data[logrotate.MySQLConfig])
		assert.True(t, metav1.IsControlledBy(cm, cr))
	})

	t.Run("no config map without a logrotate configuration", func(t *testing.T) {
		cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{Schedule: "0 0 * * *"}
		})
		cl := buildFakeClient(t, cr)

		require.NoError(t, Reconcile(t.Context(), cl, cr))

		_, err := getConfigMap(t.Context(), cl, name)
		assert.True(t, k8serrors.IsNotFound(err))
	})

	t.Run("deletes an owned config map when the configuration is cleared", func(t *testing.T) {
		cr := testCR()
		existing := ownedConfigMap(t, cr, name, map[string]string{logrotate.MySQLConfig: "old"})
		cl := buildFakeClient(t, cr, existing)

		require.NoError(t, Reconcile(t.Context(), cl, cr))

		_, err := getConfigMap(t.Context(), cl, name)
		assert.True(t, k8serrors.IsNotFound(err))
	})
}

func TestReconcileIsIdempotent(t *testing.T) {
	cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
		cr.Spec.LogCollector.Configuration = "pipeline: {}"
		cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{Configuration: "/x {}"}
	})
	cl := buildFakeClient(t, cr)

	require.NoError(t, Reconcile(t.Context(), cl, cr))

	first, err := getConfigMap(t.Context(), cl, ConfigMapName(testClusterName))
	require.NoError(t, err)

	require.NoError(t, Reconcile(t.Context(), cl, cr))

	second, err := getConfigMap(t.Context(), cl, ConfigMapName(testClusterName))
	require.NoError(t, err)

	assert.Equal(t, first.ResourceVersion, second.ResourceVersion, "second reconcile must not rewrite the config map")
}

func TestConfigHash(t *testing.T) {
	ctx := t.Context()

	t.Run("empty when disabled", func(t *testing.T) {
		cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.Enabled = new(false)
		})
		cl := buildFakeClient(t, cr)

		got, err := ConfigHash(ctx, cl, cr)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("stable across calls", func(t *testing.T) {
		cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.Configuration = "pipeline: {}"
		})
		cl := buildFakeClient(t, cr)

		first, err := ConfigHash(ctx, cl, cr)
		require.NoError(t, err)
		second, err := ConfigHash(ctx, cl, cr)
		require.NoError(t, err)

		assert.NotEmpty(t, first)
		assert.Equal(t, first, second)
	})

	t.Run("changes with the fluent-bit configuration", func(t *testing.T) {
		cl := buildFakeClient(t)

		a, err := ConfigHash(ctx, cl, testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.Configuration = "a"
		}))
		require.NoError(t, err)
		b, err := ConfigHash(ctx, cl, testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.Configuration = "b"
		}))
		require.NoError(t, err)

		assert.NotEqual(t, a, b)
	})

	t.Run("changes with the logrotate configuration and schedule", func(t *testing.T) {
		cl := buildFakeClient(t)

		base, err := ConfigHash(ctx, cl, testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{Configuration: "/x {}", Schedule: "0 0 * * *"}
		}))
		require.NoError(t, err)

		otherConfig, err := ConfigHash(ctx, cl, testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{Configuration: "/y {}", Schedule: "0 0 * * *"}
		}))
		require.NoError(t, err)

		otherSchedule, err := ConfigHash(ctx, cl, testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{Configuration: "/x {}", Schedule: "30 3 * * *"}
		}))
		require.NoError(t, err)

		assert.NotEqual(t, base, otherConfig)
		assert.NotEqual(t, base, otherSchedule)
	})

	t.Run("changes with the extra config map contents", func(t *testing.T) {
		cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{
				ExtraConfig: corev1.LocalObjectReference{Name: "extra"},
			}
		})
		extra := &corev1.ConfigMap{
			Name: "extra", Namespace: testNamespace,
			Data: map[string]string{"a.conf": "first"},
		}
		cl := buildFakeClient(t, cr, extra)

		before, err := ConfigHash(ctx, cl, cr)
		require.NoError(t, err)

		extra.Data["a.conf"] = "second"
		require.NoError(t, cl.Update(ctx, extra))

		after, err := ConfigHash(ctx, cl, cr)
		require.NoError(t, err)

		assert.NotEqual(t, before, after)
	})

	t.Run("changes with the extra config map binary contents", func(t *testing.T) {
		cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{
				ExtraConfig: corev1.LocalObjectReference{Name: "extra"},
			}
		})
		extra := &corev1.ConfigMap{
			Name: "extra", Namespace: testNamespace,
			BinaryData: map[string][]byte{"a.conf": []byte("first")},
		}
		cl := buildFakeClient(t, cr, extra)

		before, err := ConfigHash(ctx, cl, cr)
		require.NoError(t, err)

		extra.BinaryData["a.conf"] = []byte("second")
		require.NoError(t, cl.Update(ctx, extra))

		after, err := ConfigHash(ctx, cl, cr)
		require.NoError(t, err)

		assert.NotEqual(t, before, after)
	})

	t.Run("missing extra config map is not an error", func(t *testing.T) {
		cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{
				ExtraConfig: corev1.LocalObjectReference{Name: "absent"},
			}
		})
		cl := buildFakeClient(t, cr)

		got, err := ConfigHash(ctx, cl, cr)
		require.NoError(t, err)
		assert.NotEmpty(t, got)
	})
}

var errBoom = goerrors.New("boom")

func failingClient(t *testing.T, fns interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()

	return interceptor.NewClient(buildFakeClient(t, objs...), fns)
}

func failGet(kind client.Object, name string) interceptor.Funcs {
	return interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name == name && reflect.TypeOf(obj) == reflect.TypeOf(kind) {
				return errBoom
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	}
}

func TestReconcileErrors(t *testing.T) {
	tests := map[string]struct {
		mutate     func(cr *apiv1.PerconaServerMySQL)
		fns        interceptor.Funcs
		wantErrMsg string
	}{
		"fluent-bit config map lookup fails": {
			mutate: func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.LogCollector.Configuration = "pipeline: {}"
			},
			fns:        failGet(new(corev1.ConfigMap), ConfigMapName(testClusterName)),
			wantErrMsg: "fluent-bit config map: get ConfigMap/" + ConfigMapName(testClusterName) + ": boom",
		},
		"fluent-bit config map delete fails": {
			fns: interceptor.Funcs{
				Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					return errBoom
				},
			},
			wantErrMsg: "fluent-bit config map: delete ConfigMap/" + ConfigMapName(testClusterName) + ": boom",
		},
		"fluent-bit config map write fails": {
			mutate: func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.LogCollector.Configuration = "pipeline: {}"
			},
			fns: interceptor.Funcs{
				Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					return errBoom
				},
			},
			wantErrMsg: "fluent-bit config map: ensure ConfigMap/" + ConfigMapName(testClusterName) +
				": patch " + testNamespace + "/" + ConfigMapName(testClusterName) + ": boom",
		},
		"logrotate config map lookup fails": {
			mutate: func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{Configuration: "/x {}"}
			},
			fns:        failGet(new(corev1.ConfigMap), logrotate.ConfigMapName(testClusterName)),
			wantErrMsg: "logrotate config map: get ConfigMap/" + logrotate.ConfigMapName(testClusterName) + ": boom",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cr := testCR(tc.mutate)

			// Pre-existing owned ConfigMaps so the delete paths have something
			// to act on.
			cl := failingClient(t, tc.fns, cr,
				ownedConfigMap(t, cr, ConfigMapName(testClusterName), map[string]string{"a": "b"}),
				ownedConfigMap(t, cr, logrotate.ConfigMapName(testClusterName), map[string]string{"a": "b"}),
			)

			err := Reconcile(t.Context(), cl, cr)

			require.ErrorIs(t, err, errBoom)
			assert.EqualError(t, err, tc.wantErrMsg)
		})
	}
}

func TestDeleteConfigMapIfExistsGetError(t *testing.T) {
	cr := testCR()
	cl := failingClient(t, failGet(new(corev1.ConfigMap), "absent"), cr)

	err := deleteConfigMapIfExists(t.Context(), cl, cr, "absent")

	require.ErrorIs(t, err, errBoom)
	assert.EqualError(t, err, "get ConfigMap/absent: boom")
}

func TestConfigHashExtraConfigMapError(t *testing.T) {
	cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
		cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{
			ExtraConfig: corev1.LocalObjectReference{Name: "extra"},
		}
	})
	cl := failingClient(t, failGet(new(corev1.ConfigMap), "extra"), cr)

	got, err := ConfigHash(t.Context(), cl, cr)

	require.ErrorIs(t, err, errBoom)
	assert.EqualError(t, err, "get ConfigMap/extra: boom")
	assert.Empty(t, got)
}
