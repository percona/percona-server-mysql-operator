package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func TestPerconaServerMySQLRestore_GetContainerOptions(t *testing.T) {
	tests := map[string]struct {
		restore  *PerconaServerMySQLRestore
		storage  *BackupStorageSpec
		expected *BackupContainerOptions
	}{
		"both restore and storage have container options": {
			restore: &PerconaServerMySQLRestore{
				Spec: PerconaServerMySQLRestoreSpec{
					ContainerOptions: &BackupContainerOptions{
						Env: []corev1.EnvVar{
							{Name: "RESTORE_ENV", Value: "test"},
						},
					},
				},
			},
			storage: &BackupStorageSpec{
				ContainerOptions: &BackupContainerOptions{
					Env: []corev1.EnvVar{
						{Name: "STORAGE_ENV", Value: "ignored"},
					},
					Args: BackupContainerArgs{
						Xtrabackup: []string{"xtrabackup-arg"},
					},
				},
			},
			expected: &BackupContainerOptions{
				Env: []corev1.EnvVar{
					{Name: "RESTORE_ENV", Value: "test"},
				},
			},
		},
		"restore has no container options, storage has container options": {
			restore: &PerconaServerMySQLRestore{
				Spec: PerconaServerMySQLRestoreSpec{
					ContainerOptions: nil,
				},
			},
			storage: &BackupStorageSpec{
				ContainerOptions: &BackupContainerOptions{
					Env: []corev1.EnvVar{
						{Name: "STORAGE_ENV", Value: "test"},
					},
				},
			},
			expected: &BackupContainerOptions{
				Env: []corev1.EnvVar{
					{Name: "STORAGE_ENV", Value: "test"},
				},
			},
		},
		"retore has container options, storage is nil": {
			restore: &PerconaServerMySQLRestore{
				Spec: PerconaServerMySQLRestoreSpec{
					ContainerOptions: &BackupContainerOptions{
						Env: []corev1.EnvVar{
							{Name: "RESTORE_ENV", Value: "test"},
						},
					},
				},
			},
			storage: nil,
			expected: &BackupContainerOptions{
				Env: []corev1.EnvVar{
					{Name: "RESTORE_ENV", Value: "test"},
				},
			},
		},
		"restore has container options, storage doesn't": {
			restore: &PerconaServerMySQLRestore{
				Spec: PerconaServerMySQLRestoreSpec{
					ContainerOptions: &BackupContainerOptions{
						Env: []corev1.EnvVar{
							{Name: "RESTORE_ENV", Value: "test"},
						},
					},
				},
			},
			storage: &BackupStorageSpec{
				ContainerOptions: nil,
			},
			expected: &BackupContainerOptions{
				Env: []corev1.EnvVar{
					{Name: "RESTORE_ENV", Value: "test"},
				},
			},
		},
		"restore has no container options, storage is nil": {
			restore: &PerconaServerMySQLRestore{
				Spec: PerconaServerMySQLRestoreSpec{
					ContainerOptions: nil,
				},
			},
			storage:  nil,
			expected: nil,
		},
		"both restore and storage have nil container options": {
			restore: &PerconaServerMySQLRestore{
				Spec: PerconaServerMySQLRestoreSpec{
					ContainerOptions: nil,
				},
			},
			storage: &BackupStorageSpec{
				ContainerOptions: nil,
			},
			expected: nil,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			result := tt.restore.GetContainerOptions(tt.storage)
			assert.Equal(t, tt.expected, result)
		})
	}
}
