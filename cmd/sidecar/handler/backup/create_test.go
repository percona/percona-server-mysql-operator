package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
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
		conf                  *xb.BackupConfig
		generatesDefaultsFile bool
		want                  []string
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
			generatesDefaultsFile: true,
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
		"defaults file with separate value is not promoted": {
			conf: &xb.BackupConfig{
				ContainerOptions: &apiv1.BackupContainerOptions{
					Args: apiv1.BackupContainerArgs{
						Xtrabackup: []string{"--defaults-file", "/etc/my.cnf", "--compress", "--parallel=2"},
					},
				},
			},
			want: append(defaultArgs, "--defaults-file", "/etc/my.cnf", "--compress", "--parallel=2"),
		},
		"defaults file without value is not promoted": {
			conf: &xb.BackupConfig{
				ContainerOptions: &apiv1.BackupContainerOptions{
					Args: apiv1.BackupContainerArgs{Xtrabackup: []string{"--defaults-file"}},
				},
			},
			want: append(defaultArgs, "--defaults-file"),
		},
		"defaults file followed by flag is not promoted": {
			conf: &xb.BackupConfig{
				ContainerOptions: &apiv1.BackupContainerOptions{
					Args: apiv1.BackupContainerArgs{Xtrabackup: []string{"--defaults-file", "--compress"}},
				},
			},
			want: append(defaultArgs, "--defaults-file", "--compress"),
		},
		"defaults file with empty equals value is not promoted": {
			conf: &xb.BackupConfig{
				ContainerOptions: &apiv1.BackupContainerOptions{
					Args: apiv1.BackupContainerArgs{Xtrabackup: []string{"--defaults-file="}},
				},
			},
			want: append(defaultArgs, "--defaults-file="),
		},
		"defaults file with separate empty value is not promoted": {
			conf: &xb.BackupConfig{
				ContainerOptions: &apiv1.BackupContainerOptions{
					Args: apiv1.BackupContainerArgs{Xtrabackup: []string{"--defaults-file", ""}},
				},
			},
			want: append(defaultArgs, "--defaults-file", ""),
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
			generatesDefaultsFile: true,
			conf: &xb.BackupConfig{
				EncryptionKeyFile: "/etc/mysql/encryption-key",
				ContainerOptions: &apiv1.BackupContainerOptions{
					Args: apiv1.BackupContainerArgs{
						Xtrabackup: []string{"--defaults-file=/etc/my.cnf", "--encrypt=AES192", "--parallel=2"},
					},
				},
				IncrementalLsn: "123:456",
			},
			want: append(
				[]string{"--defaults-file=/etc/my.cnf"},
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
			generatedDefaultsFile, err := generateDefaultsFile(test.conf)
			assert.NoError(t, err)
			got := xtrabackupArgs("backup-user", "backup-password", test.conf, generatedDefaultsFile)
			if test.generatesDefaultsFile {
				if !assert.NotEmpty(t, generatedDefaultsFile) {
					return
				}
				t.Cleanup(func() {
					os.Remove(generatedDefaultsFile) //nolint:errcheck
				})
				assert.Equal(t, "--defaults-file="+generatedDefaultsFile, got[0])
				got[0] = test.want[0]
			} else {
				assert.Empty(t, generatedDefaultsFile)
			}
			assert.Equal(t, test.want, got)
		})
	}
}

func TestGenerateDefaultsFile(t *testing.T) {
	defaultsFile := filepath.Join(t.TempDir(), "backup.cnf")

	assert.NoError(t, os.WriteFile(defaultsFile, []byte("[xtrabackup]\nparallel=2\n"), 0o600))

	wantIncludes := make([]string, 0, 2)
	if hasCustomConfig() {
		wantIncludes = append(wantIncludes, mysql.CustomMyCnfPath)
	}
	wantIncludes = append(wantIncludes, defaultsFile)

	tests := map[string]struct {
		conf          *xb.BackupConfig
		wantGenerated bool
		wantIncludes  []string
	}{
		"nil config": {},
		"no defaults file": {
			conf: &xb.BackupConfig{},
		},
		"user defaults file": {
			conf: &xb.BackupConfig{
				ContainerOptions: &apiv1.BackupContainerOptions{
					Args: apiv1.BackupContainerArgs{
						Xtrabackup: []string{"--defaults-file=" + defaultsFile, "--compress"},
					},
				},
			},
			wantGenerated: true,
			wantIncludes:  wantIncludes,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			generatedDefaultsFile, err := generateDefaultsFile(test.conf)
			if !assert.NoError(t, err) {
				return
			}
			if !test.wantGenerated {
				assert.Empty(t, generatedDefaultsFile)
				return
			}
			if !assert.NotEmpty(t, generatedDefaultsFile) {
				return
			}
			t.Cleanup(func() {
				os.Remove(generatedDefaultsFile) //nolint:errcheck
			})

			args := xtrabackupArgs("backup-user", "backup-password", test.conf, generatedDefaultsFile)
			assert.Equal(t, "--defaults-file="+generatedDefaultsFile, args[0])
			assert.NotContains(t, args, "--defaults-extra-file="+mysql.CustomMyCnfPath)
			assert.Contains(t, args, "--compress")

			contents, err := os.ReadFile(generatedDefaultsFile)
			assert.NoError(t, err)
			includes := make([]string, 0, len(test.wantIncludes))
			for _, includePath := range test.wantIncludes {
				includes = append(includes, "!include "+includePath)
			}
			assert.Equal(t, strings.Join(includes, "\n")+"\n", string(contents))

			assert.Equal(t, "--defaults-file="+defaultsFile, test.conf.ContainerOptions.Args.Xtrabackup[0], "input config must not be mutated")
		})
	}
}
