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

func TestDestination_BackupName(t *testing.T) {
	tests := map[string]struct {
		dest     BackupDestination
		expected string
	}{
		"s3": {
			dest:     BackupDestination("s3://bucket/prefix/cluster-2026-03-23-08:40:16-full"),
			expected: "cluster-2026-03-23-08:40:16-full",
		},
		"s3 incremental": {
			dest:     BackupDestination("s3://bucket/prefix/cluster-2026-03-23-08:40:16-full.incr/cluster-2026-03-23-09:09:47-incr"),
			expected: "cluster-2026-03-23-09:09:47-incr",
		},
		"gcs": {
			dest:     BackupDestination("gs://bucket/prefix/cluster-2026-03-23-08:40:16-full"),
			expected: "cluster-2026-03-23-08:40:16-full",
		},
		"azure": {
			dest:     BackupDestination("container/prefix/cluster-2026-03-23-08:40:16-full"),
			expected: "cluster-2026-03-23-08:40:16-full",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			result := tt.dest.BackupName()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDestination_BucketAndPrefix(t *testing.T) {
	tests := map[string]struct {
		dest           BackupDestination
		expectedBucket string
		expectedPrefix string
	}{
		"s3": {
			dest:           BackupDestination("s3://bucket/prefix/cluster-2026-03-23-08:40:16-full"),
			expectedBucket: "bucket",
			expectedPrefix: "prefix/",
		},
		"s3 incremental": {
			dest:           BackupDestination("s3://bucket/prefix/cluster-2026-03-23-08:40:16-full.incr/cluster-2026-03-23-09:09:47-incr"),
			expectedBucket: "bucket",
			expectedPrefix: "prefix/cluster-2026-03-23-08:40:16-full.incr/",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			bucket, prefix := tt.dest.BucketAndPrefix()
			assert.Equal(t, tt.expectedBucket, bucket)
			assert.Equal(t, tt.expectedPrefix, prefix)
		})
	}
}
