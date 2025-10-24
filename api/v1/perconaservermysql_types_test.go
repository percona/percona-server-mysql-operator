package v1

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestCheckNSetDefaults(t *testing.T) {
	t.Run("empty cr", func(t *testing.T) {
		cr := new(PerconaServerMySQL)

		err := cr.CheckNSetDefaults(t.Context(), nil)
		assert.EqualError(t, err, "backup.image can't be empty")
	})
	t.Run("with backup image", func(t *testing.T) {
		cr := new(PerconaServerMySQL)
		cr.Spec.Backup = &BackupSpec{
			Image: "backup-image",
		}

		err := cr.CheckNSetDefaults(t.Context(), nil)
		assert.EqualError(t, err, "reconcile mysql volumeSpec: volumeSpec provided is nil")
	})
	t.Run("with backup image and volume spec", func(t *testing.T) {
		cr := new(PerconaServerMySQL)
		cr.Spec.Backup = &BackupSpec{
			Image: "backup-image",
		}
		cr.Spec.MySQL.VolumeSpec = &VolumeSpec{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1G"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1G"),
					},
				},
			},
		}

		err := cr.CheckNSetDefaults(t.Context(), nil)
		assert.NoError(t, err)
	})
}

func TestValidateVolume(t *testing.T) {
	tests := map[string]struct {
		input       *VolumeSpec
		expected    *VolumeSpec
		expectedErr string
	}{
		"nil volume": {
			expectedErr: "volumeSpec provided is nil",
		},
		"empty volume": {
			input:       &VolumeSpec{},
			expectedErr: "volumeSpec must specify at least one volume source",
		},
		"multiple volumes provided": {
			input: &VolumeSpec{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
				HostPath: &corev1.HostPathVolumeSource{},
			},
			expectedErr: "volumeSpec must specify at most one volume source — multiple sources set",
		},
		"valid emptyDir volume": {
			input:    &VolumeSpec{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			expected: &VolumeSpec{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
		"valid hostpath volume": {
			input:    &VolumeSpec{HostPath: &corev1.HostPathVolumeSource{}},
			expected: &VolumeSpec{HostPath: &corev1.HostPathVolumeSource{}},
		},
		"invalid pvc - no limits or requests specified": {
			input: &VolumeSpec{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				},
			},
			expectedErr: "pvc's resources.limits[storage] or resources.requests[storage] should be specified",
		},
		"valid pvc": {
			input: &VolumeSpec{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
			expected: &VolumeSpec{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
		},
		"valid pvc without access mode defined": {
			input: &VolumeSpec{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
			expected: &VolumeSpec{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := tc.input.validateVolume()

			if tc.expectedErr != "" {
				assert.EqualError(t, err, tc.expectedErr)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expected, got)
			}
		})
	}
}

func TestGetTerminationGracePeriodSeconds(t *testing.T) {
	tests := map[string]struct {
		input    *int64
		expected int64
	}{
		"custom grace period": {
			input:    to.Ptr(int64(20)),
			expected: 20,
		},
		"nil grace period (default used)": {
			input:    nil,
			expected: 600,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			spec := PodSpec{
				TerminationGracePeriodSeconds: tc.input,
			}
			result := spec.GetTerminationGracePeriodSeconds()
			assert.Equal(t, tc.expected, *result)
		})
	}
}
