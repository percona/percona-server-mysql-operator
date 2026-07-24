package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	xb "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup"
)

func TestXtrabackupArgs(t *testing.T) {
	defaultArgs := []string{
		"--backup",
		"--stream=xbstream",
		"--safe-slave-backup",
		"--slave-info",
		"--target-dir=/backup/",
		"--databases-exclude=lost+found",
		"--user=backup-user",
		"--password=backup-password",
	}

	tests := map[string]struct {
		conf *xb.BackupConfig
		want []string
	}{
		"nil config": {
			want: defaultArgs,
		},
		"empty config": {
			conf: &xb.BackupConfig{},
			want: defaultArgs,
		},
		"empty container options": {
			conf: &xb.BackupConfig{
				ContainerOptions: &apiv1.BackupContainerOptions{},
			},
			want: defaultArgs,
		},
		"custom arguments": {
			conf: &xb.BackupConfig{
				ContainerOptions: &apiv1.BackupContainerOptions{
					Args: apiv1.BackupContainerArgs{Xtrabackup: []string{"--compress", "--parallel=2"}},
				},
			},
			want: append(defaultArgs, "--compress", "--parallel=2"),
		},
		"defaults file with equals is first": {
			conf: &xb.BackupConfig{
				ContainerOptions: &apiv1.BackupContainerOptions{
					Args: apiv1.BackupContainerArgs{
						Xtrabackup: []string{"--defaults-file=/etc/my.cnf", "--compress", "--parallel=2"},
					},
				},
			},
			want: append(
				[]string{"--defaults-file=/etc/my.cnf"},
				append(defaultArgs, "--compress", "--parallel=2")...,
			),
		},
		"defaults file with separate value is first": {
			conf: &xb.BackupConfig{
				ContainerOptions: &apiv1.BackupContainerOptions{
					Args: apiv1.BackupContainerArgs{
						Xtrabackup: []string{"--defaults-file", "/etc/my.cnf", "--compress", "--parallel=2"},
					},
				},
			},
			want: append(
				[]string{"--defaults-file", "/etc/my.cnf"},
				append(defaultArgs, "--compress", "--parallel=2")...,
			),
		},
		"defaults file without value is first": {
			conf: &xb.BackupConfig{
				ContainerOptions: &apiv1.BackupContainerOptions{
					Args: apiv1.BackupContainerArgs{Xtrabackup: []string{"--defaults-file"}},
				},
			},
			want: append([]string{"--defaults-file"}, defaultArgs...),
		},
		"encryption uses default algorithm": {
			conf: &xb.BackupConfig{
				EncryptionKeyFile: "/etc/mysql/encryption-key",
			},
			want: append(
				defaultArgs,
				"--encrypt-key-file=/etc/mysql/encryption-key",
				"--encrypt=AES256",
			),
		},
		"custom encryption algorithm overrides default": {
			conf: &xb.BackupConfig{
				EncryptionKeyFile: "/etc/mysql/encryption-key",
				ContainerOptions: &apiv1.BackupContainerOptions{
					Args: apiv1.BackupContainerArgs{Xtrabackup: []string{"--encrypt=AES192"}},
				},
			},
			want: append(
				defaultArgs,
				"--encrypt-key-file=/etc/mysql/encryption-key",
				"--encrypt=AES192",
			),
		},
		"incremental backup": {
			conf: &xb.BackupConfig{
				IncrementalLsn: "123:456",
			},
			want: append(defaultArgs, "--incremental-lsn=123:456"),
		},
		"all optional arguments preserve required ordering": {
			conf: &xb.BackupConfig{
				EncryptionKeyFile: "/etc/mysql/encryption-key",
				ContainerOptions: &apiv1.BackupContainerOptions{
					Args: apiv1.BackupContainerArgs{
						Xtrabackup: []string{"--defaults-file", "/etc/my.cnf", "--encrypt=AES192", "--parallel=2"},
					},
				},
				IncrementalLsn: "123:456",
			},
			want: append(
				[]string{"--defaults-file", "/etc/my.cnf"},
				append(
					defaultArgs,
					"--encrypt-key-file=/etc/mysql/encryption-key",
					"--encrypt=AES192",
					"--parallel=2",
					"--incremental-lsn=123:456",
				)...,
			),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, test.want, xtrabackupArgs("backup-user", "backup-password", test.conf))
		})
	}
}
