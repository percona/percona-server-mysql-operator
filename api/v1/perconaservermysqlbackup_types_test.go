package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestDestination_IsIncremental(t *testing.T) {
	tests := map[string]struct {
		dest     BackupDestination
		expected bool
	}{
		"full s3 backup": {
			dest:     BackupDestination("s3://bucket/prefix/cluster-2026-03-23-08:40:16-full"),
			expected: false,
		},
		"incremental s3 backup": {
			dest:     BackupDestination("s3://bucket/prefix/cluster-2026-03-23-08:40:16-full.incr/cluster-2026-03-23-09:09:47-incr"),
			expected: true,
		},
		"full gcs backup": {
			dest:     BackupDestination("gs://bucket/prefix/cluster-2026-03-23-08:40:16-full"),
			expected: false,
		},
		"incremental gcs backup": {
			dest:     BackupDestination("gs://bucket/prefix/cluster-2026-03-23-08:40:16-full.incr/cluster-2026-03-23-09:09:47-incr"),
			expected: true,
		},
		"full azure backup": {
			dest:     BackupDestination("container/prefix/cluster-2026-03-23-08:40:16-full"),
			expected: false,
		},
		"incremental azure backup": {
			dest:     BackupDestination("container/prefix/cluster-2026-03-23-08:40:16-full.incr/cluster-2026-03-23-09:09:47-incr"),
			expected: true,
		},
		"empty destination": {
			dest:     BackupDestination(""),
			expected: false,
		},
		".incr in bucket name": {
			dest:     BackupDestination("s3://my-bucket.incr/prefix/cluster-full"),
			expected: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			result := tt.dest.IsIncremental()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDestination_IncrementalBaseDestination(t *testing.T) {
	tests := map[string]struct {
		dest     BackupDestination
		expected BackupDestination
	}{
		"incremental s3 backup": {
			dest:     BackupDestination("s3://bucket/prefix/weekly-full-1.incr/2026-03-17T000000"),
			expected: BackupDestination("s3://bucket/prefix/weekly-full-1"),
		},
		"incremental gcs backup": {
			dest:     BackupDestination("gs://bucket/prefix/weekly-full-1.incr/2026-03-17T000000"),
			expected: BackupDestination("gs://bucket/prefix/weekly-full-1"),
		},
		"incremental azure backup": {
			dest:     BackupDestination("container/prefix/weekly-full-1.incr/2026-03-17T000000"),
			expected: BackupDestination("container/prefix/weekly-full-1"),
		},
		"full s3 backup returns unchanged": {
			dest:     BackupDestination("s3://bucket/prefix/cluster-2026-03-23-08:40:16-full"),
			expected: BackupDestination("s3://bucket/prefix/cluster-2026-03-23-08:40:16-full"),
		},
		"full gcs backup returns unchanged": {
			dest:     BackupDestination("gs://bucket/prefix/cluster-2026-03-23-08:40:16-full"),
			expected: BackupDestination("gs://bucket/prefix/cluster-2026-03-23-08:40:16-full"),
		},
		"empty destination": {
			dest:     BackupDestination(""),
			expected: BackupDestination(""),
		},
		"multiple .incr segments uses first occurrence": {
			dest:     BackupDestination("s3://bucket/prefix/base.incr/sub.incr/timestamp"),
			expected: BackupDestination("s3://bucket/prefix/base"),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			result := tt.dest.IncrementalBaseDestination()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDestination_IncrementalsDir(t *testing.T) {
	tests := map[string]struct {
		dest     BackupDestination
		expected string
	}{
		"incremental s3 backup": {
			dest:     BackupDestination("s3://bucket/prefix/weekly-full-1.incr/2026-03-17T000000"),
			expected: "s3://bucket/prefix/weekly-full-1.incr/",
		},
		"incremental gcs backup": {
			dest:     BackupDestination("gs://bucket/prefix/weekly-full-1.incr/2026-03-17T000000"),
			expected: "gs://bucket/prefix/weekly-full-1.incr/",
		},
		"incremental azure backup": {
			dest:     BackupDestination("container/prefix/weekly-full-1.incr/2026-03-17T000000"),
			expected: "container/prefix/weekly-full-1.incr/",
		},
		"full backup returns empty": {
			dest:     BackupDestination("s3://bucket/prefix/cluster-2026-03-23-08:40:16-full"),
			expected: "",
		},
		"empty destination": {
			dest:     BackupDestination(""),
			expected: "",
		},
		"multiple .incr segments uses first occurrence": {
			dest:     BackupDestination("s3://bucket/prefix/base.incr/sub.incr/timestamp"),
			expected: "s3://bucket/prefix/base.incr/",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			result := tt.dest.IncrementalsDir()
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

func TestPerconaServerMySQLBackupStatus_Equals(t *testing.T) {
	base := PerconaServerMySQLBackupStatus{
		Type:         BackupTypeFull,
		State:        BackupSucceeded,
		StateDesc:    "completed successfully",
		Destination:  BackupDestination("s3://bucket/prefix/backup-1"),
		Image:        "percona/xtrabackup:8.0",
		BackupSource: "cluster-mysql-0",
	}

	tests := map[string]struct {
		a        PerconaServerMySQLBackupStatus
		b        PerconaServerMySQLBackupStatus
		expected bool
	}{
		"all fields equal": {
			a:        base,
			b:        base,
			expected: true,
		},
		"both zero value": {
			a:        PerconaServerMySQLBackupStatus{},
			b:        PerconaServerMySQLBackupStatus{},
			expected: true,
		},
		"different Type": {
			a: base,
			b: func() PerconaServerMySQLBackupStatus {
				s := base
				s.Type = BackupTypeIncremental
				return s
			}(),
			expected: false,
		},
		"different State": {
			a: base,
			b: func() PerconaServerMySQLBackupStatus {
				s := base
				s.State = BackupRunning
				return s
			}(),
			expected: false,
		},
		"different StateDesc": {
			a: base,
			b: func() PerconaServerMySQLBackupStatus {
				s := base
				s.StateDesc = "something else"
				return s
			}(),
			expected: false,
		},
		"different Destination": {
			a: base,
			b: func() PerconaServerMySQLBackupStatus {
				s := base
				s.Destination = BackupDestination("gs://other-bucket/backup-2")
				return s
			}(),
			expected: false,
		},
		"different Image": {
			a: base,
			b: func() PerconaServerMySQLBackupStatus {
				s := base
				s.Image = "percona/xtrabackup:8.4"
				return s
			}(),
			expected: false,
		},
		"different BackupSource": {
			a: base,
			b: func() PerconaServerMySQLBackupStatus {
				s := base
				s.BackupSource = "cluster-mysql-1"
				return s
			}(),
			expected: false,
		},
		"ignored fields differ (Storage and CompletedAt)": {
			a: base,
			b: func() PerconaServerMySQLBackupStatus {
				s := base
				s.Storage = &BackupStorageSpec{}
				now := metav1.Now()
				s.CompletedAt = &now
				return s
			}(),
			expected: true,
		},
		"different Compressed": {
			a: base,
			b: func() PerconaServerMySQLBackupStatus {
				s := base
				s.Compressed = true
				return s
			}(),
			expected: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			result := tt.a.Equals(&tt.b)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPerconaServerMySQLBackup_IsCompressed(t *testing.T) {
	tests := map[string]struct {
		backup   *PerconaServerMySQLBackup
		storage  *BackupStorageSpec
		expected bool
	}{
		"compress flag in backup spec args": {
			backup: &PerconaServerMySQLBackup{
				Spec: PerconaServerMySQLBackupSpec{
					ContainerOptions: &BackupContainerOptions{
						Args: BackupContainerArgs{
							Xtrabackup: []string{"--compress", "--compress-threads=4"},
						},
					},
				},
			},
			expected: true,
		},
		"compress flag with algorithm value": {
			backup: &PerconaServerMySQLBackup{
				Spec: PerconaServerMySQLBackupSpec{
					ContainerOptions: &BackupContainerOptions{
						Args: BackupContainerArgs{
							Xtrabackup: []string{"--compress=lz4"},
						},
					},
				},
			},
			expected: true,
		},
		"compress flag in storage args": {
			backup: &PerconaServerMySQLBackup{
				Spec: PerconaServerMySQLBackupSpec{
					ContainerOptions: nil,
				},
			},
			storage: &BackupStorageSpec{
				ContainerOptions: &BackupContainerOptions{
					Args: BackupContainerArgs{
						Xtrabackup: []string{"--compress"},
					},
				},
			},
			expected: true,
		},
		"spec args take precedence, no compress there": {
			backup: &PerconaServerMySQLBackup{
				Spec: PerconaServerMySQLBackupSpec{
					ContainerOptions: &BackupContainerOptions{
						Args: BackupContainerArgs{
							Xtrabackup: []string{"--some-other-flag"},
						},
					},
				},
			},
			storage: &BackupStorageSpec{
				ContainerOptions: &BackupContainerOptions{
					Args: BackupContainerArgs{
						Xtrabackup: []string{"--compress"},
					},
				},
			},
			expected: false,
		},
		"compress-threads alone does not count": {
			backup: &PerconaServerMySQLBackup{
				Spec: PerconaServerMySQLBackupSpec{
					ContainerOptions: &BackupContainerOptions{
						Args: BackupContainerArgs{
							Xtrabackup: []string{"--compress-threads=4"},
						},
					},
				},
			},
			expected: false,
		},
		"no container options": {
			backup: &PerconaServerMySQLBackup{
				Spec: PerconaServerMySQLBackupSpec{},
			},
			storage:  nil,
			expected: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			result := tt.backup.IsCompressed(tt.storage)
			assert.Equal(t, tt.expected, result)
		})
	}
}
