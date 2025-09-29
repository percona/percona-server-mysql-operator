package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func TestPerconaServerMySQLBackup_GetContainerOptions(t *testing.T) {
	tests := map[string]struct {
		backup   *PerconaServerMySQLBackup
		storage  *BackupStorageSpec
		expected *BackupContainerOptions
	}{
		"both backup and storage have container options": {
			backup: &PerconaServerMySQLBackup{
				Spec: PerconaServerMySQLBackupSpec{
					ContainerOptions: &BackupContainerOptions{
						Env: []corev1.EnvVar{
							{Name: "BACKUP_ENV", Value: "test"},
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
					{Name: "BACKUP_ENV", Value: "test"},
				},
			},
		},
		"backup has no container options, storage has container options": {
			backup: &PerconaServerMySQLBackup{
				Spec: PerconaServerMySQLBackupSpec{
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
		"backup has container options, storage is nil": {
			backup: &PerconaServerMySQLBackup{
				Spec: PerconaServerMySQLBackupSpec{
					ContainerOptions: &BackupContainerOptions{
						Env: []corev1.EnvVar{
							{Name: "BACKUP_ENV", Value: "test"},
						},
					},
				},
			},
			storage: nil,
			expected: &BackupContainerOptions{
				Env: []corev1.EnvVar{
					{Name: "BACKUP_ENV", Value: "test"},
				},
			},
		},
		"backup has container options, storage doesn't": {
			backup: &PerconaServerMySQLBackup{
				Spec: PerconaServerMySQLBackupSpec{
					ContainerOptions: &BackupContainerOptions{
						Env: []corev1.EnvVar{
							{Name: "BACKUP_ENV", Value: "test"},
						},
					},
				},
			},
			storage: &BackupStorageSpec{
				ContainerOptions: nil,
			},
			expected: &BackupContainerOptions{
				Env: []corev1.EnvVar{
					{Name: "BACKUP_ENV", Value: "test"},
				},
			},
		},
		"backup has no container options, storage is nil": {
			backup: &PerconaServerMySQLBackup{
				Spec: PerconaServerMySQLBackupSpec{
					ContainerOptions: nil,
				},
			},
			storage:  nil,
			expected: nil,
		},
		"both backup and storage have nil container options": {
			backup: &PerconaServerMySQLBackup{
				Spec: PerconaServerMySQLBackupSpec{
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
			result := tt.backup.GetContainerOptions(tt.storage)
			assert.Equal(t, tt.expected, result)
		})
	}
}
