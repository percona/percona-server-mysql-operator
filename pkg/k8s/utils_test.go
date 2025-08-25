package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

func TestEnsureService(t *testing.T) {
	scheme := runtime.NewScheme()
	err := corev1.AddToScheme(scheme)
	require.NoError(t, err)
	err = apiv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)

	tests := map[string]struct {
		cr          *apiv1alpha1.PerconaServerMySQL
		svc         *corev1.Service
		existingSvc *corev1.Service
		saveOldMeta bool
		expectError bool
		validate    func(t *testing.T, cl client.Client, svc *corev1.Service)
	}{
		"no ignore annotations or labels, saveOldMeta false": {
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cr",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{},
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
					Name: svc.Name, Namespace: svc.Namespace}, result)
				assert.NoError(t, err)
				assert.Equal(t, "test-service", result.Name)
				assert.Equal(t, "default", result.Namespace)
				assert.NotEmpty(t, result.GetAnnotations()["percona.com/last-config-hash"])
			},
		},
		"service doesn't exist - creates new service": {
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cr",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
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
					Name: svc.Name, Namespace: svc.Namespace}, result)
				assert.NoError(t, err)
				assert.Equal(t, "new-service", result.Name)
				assert.Contains(t, result.GetAnnotations(), "new")
			},
		},
		"service exists - preserves old metadata when saveOldMeta is true": {
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cr",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{},
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
					Name: svc.Name, Namespace: svc.Namespace}, result)
				assert.NoError(t, err)
				assert.Contains(t, result.GetAnnotations(), "old")
				assert.Contains(t, result.GetAnnotations(), "new")
				assert.Contains(t, result.GetLabels(), "old")
				assert.Contains(t, result.GetLabels(), "new")
			},
		},
		"service exists - dont preserve old metadata when saveOldMeta is true": {
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cr",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{},
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
					Name: svc.Name, Namespace: svc.Namespace}, result)
				assert.NoError(t, err)
				assert.NotContains(t, result.GetAnnotations(), "old")
				assert.NotContains(t, result.GetLabels(), "old")
			},
		},
		"service exists - handles ignored annotations": {
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cr",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
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
					Name: svc.Name, Namespace: svc.Namespace}, result)
				assert.NoError(t, err)
				assert.Contains(t, result.GetAnnotations(), "ignore.annotation")
				assert.Contains(t, result.GetAnnotations(), "keep")
				assert.NotContains(t, result.GetAnnotations(), "remove")
			},
		},
		"service exists - handles ignored labels": {
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cr",
					Namespace: "default",
					UID:       "test-uid",
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
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
					Name: svc.Name, Namespace: svc.Namespace}, result)
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
