package logcollector

import (
	"context"
	goerrors "errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
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
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

const testNamespace = "ns"

var testMySQLSTS = types.NamespacedName{Name: "cluster1-mysql", Namespace: testNamespace}

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

func TestResolveDefaultEnabled(t *testing.T) {
	tests := map[string]struct {
		enabled     *bool
		stsExists   bool
		annotation  string
		wantEnabled *bool
	}{
		"recorded decision wins over the statefulset check": {
			annotation:  "false",
			wantEnabled: new(false),
		},
		"recorded decision survives once the statefulset exists": {
			stsExists:   true,
			annotation:  "true",
			wantEnabled: new(true),
		},
		"new cluster defaults to on": {
			wantEnabled: new(true),
		},
		"existing cluster defaults to off": {
			stsExists:   true,
			wantEnabled: new(false),
		},
		"explicit true is kept on an existing cluster": {
			enabled:     new(true),
			stsExists:   true,
			wantEnabled: new(true),
		},
		"explicit false is kept on a new cluster": {
			enabled:     new(false),
			wantEnabled: new(false),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
				cr.Spec.LogCollector.Enabled = tc.enabled
				if tc.annotation != "" {
					cr.Annotations = map[string]string{
						string(naming.AnnotationLogCollectorDefaulted): tc.annotation,
					}
				}
			})

			objs := []client.Object{cr}
			if tc.stsExists {
				objs = append(objs, &appsv1.StatefulSet{
					Name: testMySQLSTS.Name, Namespace: testMySQLSTS.Namespace,
				})
			}
			cl := buildFakeClient(t, objs...)

			require.NoError(t, Reconcile(t.Context(), cl, cr, testMySQLSTS))
			require.NotNil(t, cr.Spec.LogCollector.Enabled)
			assert.Equal(t, *tc.wantEnabled, *cr.Spec.LogCollector.Enabled)
		})
	}
}

func TestReconcileSpecAbsent(t *testing.T) {
	cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
		cr.Spec.LogCollector = nil
	})
	cl := buildFakeClient(t, cr)

	require.NoError(t, Reconcile(t.Context(), cl, cr, testMySQLSTS))
	assert.Nil(t, cr.Spec.LogCollector)
}

func TestReconcileSkipsOldCRVersion(t *testing.T) {
	cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
		cr.Spec.CRVersion = testOldCRVersion
		cr.Spec.LogCollector.Enabled = nil
		cr.Spec.LogCollector.Configuration = "pipeline: {}"
	})
	cl := buildFakeClient(t, cr)

	require.NoError(t, Reconcile(t.Context(), cl, cr, testMySQLSTS))

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

		require.NoError(t, Reconcile(t.Context(), cl, cr, testMySQLSTS))

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

		require.NoError(t, Reconcile(t.Context(), cl, cr, testMySQLSTS))

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

		require.NoError(t, Reconcile(t.Context(), cl, cr, testMySQLSTS))

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

		require.NoError(t, Reconcile(t.Context(), cl, cr, testMySQLSTS))

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

		require.NoError(t, Reconcile(t.Context(), cl, cr, testMySQLSTS))

		cm, err := getConfigMap(t.Context(), cl, ConfigMapName(testClusterName))
		require.NoError(t, err)
		assert.Equal(t, "me", cm.Data["keep"])
	})
}

func TestReconcileLogRotateConfigMap(t *testing.T) {
	name := logrotate.ConfigMapName(testClusterName)

	t.Run("creates the config map", func(t *testing.T) {
		cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{Configuration: "/x {}"}
		})
		cl := buildFakeClient(t, cr)

		require.NoError(t, Reconcile(t.Context(), cl, cr, testMySQLSTS))

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

		require.NoError(t, Reconcile(t.Context(), cl, cr, testMySQLSTS))

		_, err := getConfigMap(t.Context(), cl, name)
		assert.True(t, k8serrors.IsNotFound(err))
	})

	t.Run("deletes an owned config map when the configuration is cleared", func(t *testing.T) {
		cr := testCR()
		existing := ownedConfigMap(t, cr, name, map[string]string{logrotate.MySQLConfig: "old"})
		cl := buildFakeClient(t, cr, existing)

		require.NoError(t, Reconcile(t.Context(), cl, cr, testMySQLSTS))

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

	require.NoError(t, Reconcile(t.Context(), cl, cr, testMySQLSTS))

	first, err := getConfigMap(t.Context(), cl, ConfigMapName(testClusterName))
	require.NoError(t, err)

	require.NoError(t, Reconcile(t.Context(), cl, cr, testMySQLSTS))

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

func TestStampConfigHash(t *testing.T) {
	key := string(naming.AnnotationLogCollectorConfigHash)

	t.Run("stamps the hash on the pod template", func(t *testing.T) {
		cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.Configuration = "pipeline: {}"
		})
		cl := buildFakeClient(t, cr)
		tmpl := new(corev1.PodTemplateSpec)

		require.NoError(t, StampConfigHash(t.Context(), cl, cr, tmpl))

		want, err := ConfigHash(t.Context(), cl, cr)
		require.NoError(t, err)
		assert.Equal(t, want, tmpl.Annotations[key])
	})

	t.Run("preserves existing annotations", func(t *testing.T) {
		cr := testCR()
		cl := buildFakeClient(t, cr)
		tmpl := &corev1.PodTemplateSpec{
			Annotations: map[string]string{"keep": "me"},
		}

		require.NoError(t, StampConfigHash(t.Context(), cl, cr, tmpl))

		assert.Equal(t, "me", tmpl.Annotations["keep"])
		assert.NotEmpty(t, tmpl.Annotations[key])
	})

	t.Run("no annotation when disabled", func(t *testing.T) {
		cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
			cr.Spec.LogCollector.Enabled = new(false)
		})
		cl := buildFakeClient(t, cr)
		tmpl := new(corev1.PodTemplateSpec)

		require.NoError(t, StampConfigHash(t.Context(), cl, cr, tmpl))

		assert.NotContains(t, tmpl.Annotations, key)
	})
}

func TestResolveDefaultEnabledIsStableAcrossReconciles(t *testing.T) {
	cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
		cr.Spec.LogCollector.Enabled = nil
	})
	cl := buildFakeClient(t, cr)

	require.NoError(t, Reconcile(t.Context(), cl, cr, testMySQLSTS))
	require.NotNil(t, cr.Spec.LogCollector.Enabled)
	require.True(t, *cr.Spec.LogCollector.Enabled, "a new cluster must default to on")

	// reconcileDatabase creates the StatefulSet after this step runs; the next
	// reconcile must not read that as "pre-existing cluster" and turn the
	// collector back off, which would roll the pods on every other reconcile.
	require.NoError(t, cl.Create(t.Context(), &appsv1.StatefulSet{
		Name: testMySQLSTS.Name, Namespace: testMySQLSTS.Namespace,
	}))

	// The controller re-reads the CR from the API on every reconcile.
	next := new(apiv1.PerconaServerMySQL)
	require.NoError(t, cl.Get(t.Context(), types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, next))
	next.Spec.LogCollector.Enabled = nil

	require.NoError(t, Reconcile(t.Context(), cl, next, testMySQLSTS))

	require.NotNil(t, next.Spec.LogCollector.Enabled)
	assert.True(t, *next.Spec.LogCollector.Enabled, "log collector must stay enabled once defaulted on")
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
		"statefulset lookup fails": {
			mutate: func(cr *apiv1.PerconaServerMySQL) { cr.Spec.LogCollector.Enabled = nil },
			fns:    failGet(new(appsv1.StatefulSet), testMySQLSTS.Name),
			wantErrMsg: "resolve log collector default: get StatefulSet/" +
				testMySQLSTS.Name + ": boom",
		},
		"recording the default fails": {
			mutate: func(cr *apiv1.PerconaServerMySQL) { cr.Spec.LogCollector.Enabled = nil },
			fns: interceptor.Funcs{
				Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					return errBoom
				},
			},
			wantErrMsg: "resolve log collector default: record log collector default: boom",
		},
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

			err := Reconcile(t.Context(), cl, cr, testMySQLSTS)

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

func TestStampConfigHashError(t *testing.T) {
	cr := testCR(func(cr *apiv1.PerconaServerMySQL) {
		cr.Spec.LogCollector.LogRotate = &apiv1.LogRotateSpec{
			ExtraConfig: corev1.LocalObjectReference{Name: "extra"},
		}
	})
	cl := failingClient(t, failGet(new(corev1.ConfigMap), "extra"), cr)
	tmpl := new(corev1.PodTemplateSpec)

	err := StampConfigHash(t.Context(), cl, cr, tmpl)

	require.ErrorIs(t, err, errBoom)
	assert.EqualError(t, err, "compute log collector config hash: get ConfigMap/extra: boom")
	assert.Empty(t, tmpl.Annotations)
}
