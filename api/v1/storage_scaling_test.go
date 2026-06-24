package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func minimalCR(t *testing.T) *PerconaServerMySQL {
	t.Helper()

	cr := new(PerconaServerMySQL)
	cr.Name = "cluster1"
	cr.Namespace = "namespace"
	cr.Spec.MySQL.VolumeSpec = &VolumeSpec{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("2Gi"),
				},
			},
		},
	}

	require.NoError(t, cr.CheckNSetDefaults(t.Context(), nil))

	return cr
}

func TestIsVolumeExpansionEnabled(t *testing.T) {
	tests := map[string]struct {
		spec     PerconaServerMySQLSpec
		expected bool
	}{
		"no storageScaling, expansion disabled": {
			spec:     PerconaServerMySQLSpec{},
			expected: false,
		},
		"no storageScaling, deprecated field enabled": {
			spec:     PerconaServerMySQLSpec{VolumeExpansionEnabled: true},
			expected: true,
		},
		"storageScaling enabled": {
			spec: PerconaServerMySQLSpec{
				StorageScaling: &StorageScalingSpec{EnableVolumeScaling: true},
			},
			expected: true,
		},
		"storageScaling disabled overrides deprecated field": {
			spec: PerconaServerMySQLSpec{
				VolumeExpansionEnabled: true,
				StorageScaling:         &StorageScalingSpec{EnableVolumeScaling: false},
			},
			expected: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.spec.IsVolumeExpansionEnabled())
		})
	}
}

func TestStorageAutoscaling(t *testing.T) {
	spec := PerconaServerMySQLSpec{}
	assert.Nil(t, spec.StorageAutoscaling())

	spec.StorageScaling = &StorageScalingSpec{}
	assert.Nil(t, spec.StorageAutoscaling())

	spec.StorageScaling.Autoscaling = &AutoscalingSpec{Enabled: true}
	assert.NotNil(t, spec.StorageAutoscaling())
	assert.True(t, spec.StorageAutoscaling().Enabled)
}

func TestCheckNSetDefaultsStorageAutoscaling(t *testing.T) {
	t.Run("defaults are set when autoscaling is configured", func(t *testing.T) {
		cr := minimalCR(t)
		cr.Spec.StorageScaling = &StorageScalingSpec{
			EnableVolumeScaling: true,
			Autoscaling:         &AutoscalingSpec{Enabled: true},
		}

		assert.NoError(t, cr.CheckNSetDefaults(t.Context(), nil))
		assert.Equal(t, 80, cr.Spec.StorageScaling.Autoscaling.TriggerThresholdPercent)
		assert.Equal(t, 0, cr.Spec.StorageScaling.Autoscaling.GrowthStep.Cmp(resource.MustParse("2Gi")))
	})
	t.Run("configured values are kept", func(t *testing.T) {
		cr := minimalCR(t)
		cr.Spec.StorageScaling = &StorageScalingSpec{
			EnableVolumeScaling: true,
			Autoscaling: &AutoscalingSpec{
				Enabled:                 true,
				TriggerThresholdPercent: 90,
				GrowthStep:              resource.MustParse("5Gi"),
				MaxSize:                 resource.MustParse("100Gi"),
			},
		}

		assert.NoError(t, cr.CheckNSetDefaults(t.Context(), nil))
		assert.Equal(t, 90, cr.Spec.StorageScaling.Autoscaling.TriggerThresholdPercent)
		assert.Equal(t, 0, cr.Spec.StorageScaling.Autoscaling.GrowthStep.Cmp(resource.MustParse("5Gi")))
	})
	t.Run("maxSize below 1Gi is rejected", func(t *testing.T) {
		cr := minimalCR(t)
		cr.Spec.StorageScaling = &StorageScalingSpec{
			EnableVolumeScaling: true,
			Autoscaling: &AutoscalingSpec{
				Enabled: true,
				MaxSize: resource.MustParse("512Mi"),
			},
		}

		err := cr.CheckNSetDefaults(t.Context(), nil)
		assert.ErrorContains(t, err, "maxSize must be at least 1Gi")
	})
	t.Run("no validation when autoscaling is disabled", func(t *testing.T) {
		cr := minimalCR(t)
		cr.Spec.StorageScaling = &StorageScalingSpec{
			EnableVolumeScaling: true,
			Autoscaling: &AutoscalingSpec{
				Enabled: false,
				MaxSize: resource.MustParse("512Mi"),
			},
		}

		assert.NoError(t, cr.CheckNSetDefaults(t.Context(), nil))
		// defaults are still applied to the disabled config
		assert.Equal(t, 80, cr.Spec.StorageScaling.Autoscaling.TriggerThresholdPercent)
	})
}
