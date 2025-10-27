package k8s

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/testutil"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
)

func TestEnsureService(t *testing.T) {
	scheme := runtime.NewScheme()
	err := corev1.AddToScheme(scheme)
	require.NoError(t, err)
	err = apiv1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := map[string]struct {
		cr          *apiv1.PerconaServerMySQL
		svc         *corev1.Service
		existingSvc *corev1.Service
		saveOldMeta bool
		expectError bool
		validate    func(t *testing.T, cl client.Client, svc *corev1.Service)
	}{
		"no ignore annotations or labels, saveOldMeta false": {
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cr",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: apiv1.PerconaServerMySQLSpec{},
			},
			svc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-service",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Port: 3306, Name: "mysql"},
					},
				},
			},
			saveOldMeta: false,
			expectError: false,
			validate: func(t *testing.T, cl client.Client, svc *corev1.Service) {
				result := &corev1.Service{}
				err := cl.Get(context.Background(), types.NamespacedName{
					Name: svc.Name, Namespace: svc.Namespace,
				}, result)
				assert.NoError(t, err)
				assert.Equal(t, "test-service", result.Name)
				assert.Equal(t, "default", result.Namespace)
				assert.NotEmpty(t, result.GetAnnotations()["percona.com/last-config-hash"])
			},
		},
		"service doesn't exist - creates new service": {
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cr",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					IgnoreAnnotations: []string{"ignore.me"},
				},
			},
			svc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "new-service",
					Namespace: "default",
					Annotations: map[string]string{
						"new": "annotation",
					},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Port: 3306, Name: "mysql"},
					},
				},
			},
			saveOldMeta: true,
			expectError: false,
			validate: func(t *testing.T, cl client.Client, svc *corev1.Service) {
				result := &corev1.Service{}
				err := cl.Get(context.Background(), types.NamespacedName{
					Name: svc.Name, Namespace: svc.Namespace,
				}, result)
				assert.NoError(t, err)
				assert.Equal(t, "new-service", result.Name)
				assert.Contains(t, result.GetAnnotations(), "new")
			},
		},
		"service exists - preserves old metadata when saveOldMeta is true": {
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cr",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: apiv1.PerconaServerMySQLSpec{},
			},
			svc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "existing-service",
					Namespace: "default",
					Annotations: map[string]string{
						"new": "annotation",
					},
					Labels: map[string]string{
						"new": "label",
					},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Port: 3306, Name: "mysql"},
					},
				},
			},
			existingSvc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "existing-service",
					Namespace: "default",
					Annotations: map[string]string{
						"old": "annotation",
					},
					Labels: map[string]string{
						"old": "label",
					},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Port: 80, Name: "http"},
					},
				},
			},
			saveOldMeta: true,
			expectError: false,
			validate: func(t *testing.T, cl client.Client, svc *corev1.Service) {
				result := &corev1.Service{}
				err := cl.Get(context.Background(), types.NamespacedName{
					Name: svc.Name, Namespace: svc.Namespace,
				}, result)
				assert.NoError(t, err)
				assert.Contains(t, result.GetAnnotations(), "old")
				assert.Contains(t, result.GetAnnotations(), "new")
				assert.Contains(t, result.GetLabels(), "old")
				assert.Contains(t, result.GetLabels(), "new")
			},
		},
		"service exists - dont preserve old metadata when saveOldMeta is true": {
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cr",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: apiv1.PerconaServerMySQLSpec{},
			},
			svc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "existing-service",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Port: 3306, Name: "mysql"},
					},
				},
			},
			existingSvc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "existing-service",
					Namespace: "default",
					Annotations: map[string]string{
						"old": "annotation",
					},
					Labels: map[string]string{
						"old": "label",
					},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Port: 80, Name: "http"},
					},
				},
			},
			saveOldMeta: false,
			expectError: false,
			validate: func(t *testing.T, cl client.Client, svc *corev1.Service) {
				result := &corev1.Service{}
				err := cl.Get(context.Background(), types.NamespacedName{
					Name: svc.Name, Namespace: svc.Namespace,
				}, result)
				assert.NoError(t, err)
				assert.NotContains(t, result.GetAnnotations(), "old")
				assert.NotContains(t, result.GetLabels(), "old")
			},
		},
		"service exists - handles ignored annotations": {
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cr",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					IgnoreAnnotations: []string{"ignore.annotation"},
				},
			},
			svc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "service-with-ignored",
					Namespace: "default",
					Annotations: map[string]string{
						"keep": "this",
					},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Port: 3306, Name: "mysql"},
					},
				},
			},
			existingSvc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "service-with-ignored",
					Namespace: "default",
					Annotations: map[string]string{
						"ignore.annotation": "should-be-kept",
						"remove":            "this",
					},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Port: 80, Name: "http"},
					},
				},
			},
			saveOldMeta: false,
			expectError: false,
			validate: func(t *testing.T, cl client.Client, svc *corev1.Service) {
				result := &corev1.Service{}
				err := cl.Get(context.Background(), types.NamespacedName{
					Name: svc.Name, Namespace: svc.Namespace,
				}, result)
				assert.NoError(t, err)
				assert.Contains(t, result.GetAnnotations(), "ignore.annotation")
				assert.Contains(t, result.GetAnnotations(), "keep")
				assert.NotContains(t, result.GetAnnotations(), "remove")
			},
		},
		"service exists - handles ignored labels": {
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cr",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					IgnoreLabels: []string{"ignore.label"},
				},
			},
			svc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "service-with-ignored-labels",
					Namespace: "default",
					Labels: map[string]string{
						"keep": "this",
					},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Port: 3306, Name: "mysql"},
					},
				},
			},
			existingSvc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "service-with-ignored-labels",
					Namespace: "default",
					Labels: map[string]string{
						"ignore.label": "should-be-kept",
						"remove":       "this",
					},
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Port: 80, Name: "http"},
					},
				},
			},
			saveOldMeta: false,
			expectError: false,
			validate: func(t *testing.T, cl client.Client, svc *corev1.Service) {
				result := &corev1.Service{}
				err := cl.Get(context.Background(), types.NamespacedName{
					Name: svc.Name, Namespace: svc.Namespace,
				}, result)
				assert.NoError(t, err)
				assert.Contains(t, result.GetLabels(), "ignore.label")
				assert.Contains(t, result.GetLabels(), "keep")
				assert.NotContains(t, result.GetLabels(), "remove")
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var objects []client.Object
			if tt.existingSvc != nil {
				objects = append(objects, tt.existingSvc)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()

			err := EnsureService(context.Background(), fakeClient, tt.cr, tt.svc, scheme, tt.saveOldMeta)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validate != nil {
					tt.validate(t, fakeClient, tt.svc)
				}
			}
		})
	}
}

func TestEqualMetadata(t *testing.T) {
	tests := []struct {
		name   string
		input  []metav1.ObjectMeta
		output bool
	}{
		{
			name:   "empty input",
			output: true,
		},
		{
			name: "single input",
			input: []metav1.ObjectMeta{
				{Name: "meta"},
			},
			output: true,
		},
		{
			name: "equal",
			input: []metav1.ObjectMeta{
				{
					Name:                       "equal-meta1",
					GenerateName:               "equal-",
					Namespace:                  "test",
					DeletionGracePeriodSeconds: new(int64),
					Labels: map[string]string{
						"test-label": "test-value",
					},
					Annotations: map[string]string{
						"test-annotation": "test-value",
					},
					Finalizers: []string{
						"test-fin-1",
					},
				},
				{
					Name:                       "equal-meta1",
					GenerateName:               "equal-",
					Namespace:                  "test",
					DeletionGracePeriodSeconds: new(int64),
					Labels: map[string]string{
						"test-label": "test-value",
					},
					Annotations: map[string]string{
						"test-annotation":                        "test-value",
						naming.AnnotationLastConfigHash.String(): "true",
					},
					Finalizers: []string{
						"test-fin-1",
					},

					SelfLink:        "selflink1",
					UID:             "uid1",
					ResourceVersion: "resourceVer1",
					Generation:      1,
					CreationTimestamp: metav1.Time{
						Time: time.Now().Add(-time.Second),
					},
					DeletionTimestamp: &metav1.Time{
						Time: time.Now().Add(time.Second),
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apiVersion1",
						},
					},
					ManagedFields: []metav1.ManagedFieldsEntry{
						{
							APIVersion: "apiVersion1",
						},
					},
				},
				{
					Name:                       "equal-meta1",
					GenerateName:               "equal-",
					Namespace:                  "test",
					DeletionGracePeriodSeconds: new(int64),
					Labels: map[string]string{
						"test-label": "test-value",
					},
					Annotations: map[string]string{
						"test-annotation":                        "test-value",
						naming.AnnotationLastConfigHash.String(): "false",
					},
					Finalizers: []string{
						"test-fin-1",
					},

					SelfLink:        "selflink2",
					UID:             "uid2",
					ResourceVersion: "resourceVer2",
					Generation:      2,
					CreationTimestamp: metav1.Time{
						Time: time.Now().Add(time.Second),
					},
					DeletionTimestamp: &metav1.Time{
						Time: time.Now().Add(-time.Second),
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apiVersion2",
						},
					},
					ManagedFields: []metav1.ManagedFieldsEntry{
						{
							APIVersion: "apiVersion2",
						},
					},
				},
			},
			output: true,
		},
		{
			name: "different names",
			input: []metav1.ObjectMeta{
				{Name: "equal-meta1"},
				{Name: "equal-meta2"},
			},
		},
		{
			name: "different generateName",
			input: []metav1.ObjectMeta{
				{GenerateName: "equal-"},
				{GenerateName: "equal2-"},
			},
		},
		{
			name: "different namespaces",
			input: []metav1.ObjectMeta{
				{Namespace: "ns"},
				{Namespace: "ns2"},
			},
		},
		{
			name: "different deletionGracePeriodSeconds",
			input: []metav1.ObjectMeta{
				{},
				{DeletionGracePeriodSeconds: new(int64)},
			},
		},
		{
			name: "different labels",
			input: []metav1.ObjectMeta{
				{Labels: map[string]string{"test-label": "test-value"}},
				{Labels: map[string]string{"test-label": "test-value2"}},
			},
		},
		{
			name: "different annotations",
			input: []metav1.ObjectMeta{
				{Annotations: map[string]string{"test-annotation": "test-value"}},
				{Annotations: map[string]string{"test-annotation": "test-value2"}},
			},
		},
		{
			name: "different finalizers",
			input: []metav1.ObjectMeta{
				{Finalizers: []string{"test-fin-1"}},
				{Finalizers: []string{"test-fin-2"}},
			},
		},
		{
			name: "nil and empty annotations",
			input: []metav1.ObjectMeta{
				{Annotations: nil},
				{Annotations: map[string]string{}},
			},
			output: true,
		},
		{
			name: "nil and empty labels",
			input: []metav1.ObjectMeta{
				{Labels: nil},
				{Labels: map[string]string{}},
			},
			output: true,
		},
	}

	for _, tt := range tests {
		t.Run(t.Name(), func(t *testing.T) {
			result := EqualMetadata(tt.input...)
			if result != tt.output {
				t.Errorf("Expected %v, got %v", tt.output, result)
			}
		})
	}
}

func TestSetCRVersion(t *testing.T) {
	ctx := context.Background()

	t.Run("CRVersion is already set", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cr-version-1",
				Namespace: "default",
			},
			Spec: apiv1.PerconaServerMySQLSpec{
				CRVersion: version.Version(),
			},
		}

		cl := testutil.BuildFakeClient(cr)

		err := setCRVersion(ctx, cl, cr)
		require.NoError(t, err)

		assert.Equal(t, version.Version(), cr.Spec.CRVersion)
	})

	t.Run("CRVersion is empty and gets patched", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cr-version-2",
				Namespace: "default",
			},
			Spec: apiv1.PerconaServerMySQLSpec{
				CRVersion: "",
			},
		}

		cl := testutil.BuildFakeClient(cr)

		err := setCRVersion(ctx, cl, cr)
		require.NoError(t, err)

		var updated apiv1.PerconaServerMySQL
		err = cl.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, &updated)
		require.NoError(t, err)
		assert.Equal(t, version.Version(), updated.Spec.CRVersion)
	})

	t.Run("Patch fails because object does not exist", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cr-version-3",
				Namespace: "default",
			},
			Spec: apiv1.PerconaServerMySQLSpec{
				CRVersion: "",
			},
		}

		cl := testutil.BuildFakeClient()

		err := setCRVersion(ctx, cl, cr)
		assert.Error(t, err)
		assert.Equal(t, "patch CR version: perconaservermysqls.ps.percona.com \"cr-version-3\" not found", err.Error())
	})
}
