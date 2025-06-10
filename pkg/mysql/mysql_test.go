package mysql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func TestPrimaryService_GroupReplication(t *testing.T) {
	cr := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-namespace",
		},
		Spec: apiv1alpha1.PerconaServerMySQLSpec{
			MySQL: apiv1alpha1.MySQLSpec{
				ClusterType: apiv1alpha1.ClusterTypeGR,
				ExposePrimary: apiv1alpha1.ServiceExposeTogglable{
					Enabled: true,
					ServiceExpose: apiv1alpha1.ServiceExpose{
						Type: corev1.ServiceTypeLoadBalancer,
						Labels: map[string]string{
							"custom-label": "custom-value",
						},
						Annotations: map[string]string{
							"custom-annotation": "custom-annotation-value",
						},
						LoadBalancerSourceRanges: []string{"10.0.0.0/8"},
					},
				},
			},
		},
	}

	tests := map[string]struct {
		serviceType                 corev1.ServiceType
		expectLoadBalancer          bool
		expectExternalTrafficPolicy bool
	}{
		"LoadBalancer service for GR": {
			serviceType:                 corev1.ServiceTypeLoadBalancer,
			expectLoadBalancer:          true,
			expectExternalTrafficPolicy: true,
		},
		"NodePort service for GR": {
			serviceType:                 corev1.ServiceTypeNodePort,
			expectLoadBalancer:          false,
			expectExternalTrafficPolicy: true,
		},
		"ClusterIP service for GR": {
			serviceType:                 corev1.ServiceTypeClusterIP,
			expectLoadBalancer:          false,
			expectExternalTrafficPolicy: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cr.Spec.MySQL.ExposePrimary.Type = tt.serviceType

			service := PrimaryService(cr)

			assert.Equal(t, "v1", service.APIVersion)
			assert.Equal(t, "Service", service.Kind)
			assert.Equal(t, PrimaryServiceName(cr), service.Name)
			assert.Equal(t, "test-cluster-mysql-primary", service.Name)
			assert.Equal(t, "test-namespace", service.Namespace)

			assert.Equal(t, tt.serviceType, service.Spec.Type)

			expectedLabels := MatchLabels(cr)
			expectedLabels["custom-label"] = "custom-value"
			assert.Equal(t, expectedLabels, service.Labels)

			expectedSelector := MatchLabels(cr)
			expectedSelector[naming.LabelMySQLPrimary] = "true"
			expectedSelector["custom-label"] = "custom-value"
			assert.Equal(t, expectedSelector, service.Spec.Selector)

			assert.Equal(t, cr.Spec.MySQL.ExposePrimary.Annotations, service.Annotations)

			if tt.expectLoadBalancer {
				assert.Equal(t, cr.Spec.MySQL.ExposePrimary.LoadBalancerSourceRanges, service.Spec.LoadBalancerSourceRanges)
			} else {
				assert.Empty(t, service.Spec.LoadBalancerSourceRanges)
			}

			if tt.expectExternalTrafficPolicy {
				assert.Equal(t, cr.Spec.MySQL.ExposePrimary.ExternalTrafficPolicy, service.Spec.ExternalTrafficPolicy)
			} else {
				assert.Empty(t, service.Spec.ExternalTrafficPolicy)
			}

			assert.Equal(t, cr.Spec.MySQL.ExposePrimary.InternalTrafficPolicy, service.Spec.InternalTrafficPolicy)
		})
	}
}

func TestPrimaryServiceName(t *testing.T) {
	cr := &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-cluster",
		},
	}
	serviceName := PrimaryServiceName(cr)
	assert.Equal(t, "my-cluster-mysql-primary", serviceName)
}
