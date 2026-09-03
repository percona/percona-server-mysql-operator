package gr

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/go-ini/ini"
	"github.com/percona/percona-server-mysql-operator/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateOptionsString(t *testing.T) {
	opts := &createClusterOpts{
		multiPrimary:       false,
		paxosSingleLeader:  true,
		communicationStack: "MYSQL",
	}
	assert.Equal(t, `{"force": false, "multiPrimary": false, "paxosSingleLeader": true, "communicationStack": "MYSQL"}`, opts.String())

	opts = &createClusterOpts{
		force:              true,
		multiPrimary:       true,
		paxosSingleLeader:  false,
		communicationStack: "XCOM",
	}
	assert.Equal(t, `{"force": true, "multiPrimary": true, "paxosSingleLeader": false, "communicationStack": "XCOM"}`, opts.String())
}

func TestGetCreateOptions(t *testing.T) {
	tests := map[string]struct {
		expected *createClusterOpts
		cnf      func() *ini.Section
	}{
		"[mysqld] section": {
			expected: &createClusterOpts{
				force:              false,
				multiPrimary:       false,
				paxosSingleLeader:  false,
				communicationStack: "MYSQL",
			},
			cnf: func() *ini.Section {
				cnf := `
				[mysqld]
				group_replication_single_primary_mode=ON
				group_replication_paxos_single_leader=OFF
				group_replication_communication_stack=MYSQL
				`
				myCnfFile := io.NopCloser(bytes.NewReader([]byte(cnf)))
				myCnf, err := config.ParseSection(myCnfFile, "mysqld")
				require.NoError(t, err)
				return myCnf
			},
		},
		"[mysqld] section with loose prefix": {
			expected: &createClusterOpts{
				force:              true,
				multiPrimary:       true,
				paxosSingleLeader:  false,
				communicationStack: "XCOM",
			},
			cnf: func() *ini.Section {
				cnf := `
				[mysqld]
				loose_group_replication_single_primary_mode=OFF
				loose_group_replication_paxos_single_leader=OFF
				loose_group_replication_communication_stack=XCOM
				`
				myCnfFile := io.NopCloser(bytes.NewReader([]byte(cnf)))
				myCnf, err := config.ParseSection(myCnfFile, "mysqld")
				require.NoError(t, err)
				return myCnf
			},
		},
		"root section": {
			expected: &createClusterOpts{
				force:              false,
				multiPrimary:       false,
				paxosSingleLeader:  true,
				communicationStack: "XCOM",
			},
			cnf: func() *ini.Section {
				cnf := `
				group_replication_single_primary_mode=ON
				group_replication_paxos_single_leader=ON
				group_replication_communication_stack=XCOM
				`
				myCnfFile := io.NopCloser(bytes.NewReader([]byte(cnf)))
				myCnf, err := config.ParseSection(myCnfFile, "mysqld")
				require.NoError(t, err)
				return myCnf
			},
		},
		"no custom config": {
			expected: &createClusterOpts{
				force:              false,
				multiPrimary:       false,
				paxosSingleLeader:  true,
				communicationStack: "MYSQL",
			},
			cnf: func() *ini.Section {
				return nil
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			opts, err := getCreateClusterOpts(tt.cnf())
			require.NoError(t, err)

			assert.Equal(t, tt.expected.force, opts.force)
			assert.Equal(t, tt.expected.multiPrimary, opts.multiPrimary)
			assert.Equal(t, tt.expected.paxosSingleLeader, opts.paxosSingleLeader)
			assert.Equal(t, tt.expected.communicationStack, opts.communicationStack)
		})
	}
}

func TestConfigureInstanceOptionsString(t *testing.T) {
	opts := &configureInstanceOpts{
		applierWorkerThreads: 4,
	}
	assert.Equal(t, `{"applierWorkerThreads": 4}`, opts.String())

	opts = &configureInstanceOpts{
		applierWorkerThreads: 16,
	}
	assert.Equal(t, `{"applierWorkerThreads": 16}`, opts.String())
}

func TestGetConfigureInstanceOpts(t *testing.T) {
	tests := map[string]struct {
		expected *configureInstanceOpts
		cnf      func() *ini.Section
	}{
		"no custom config": {
			expected: &configureInstanceOpts{
				applierWorkerThreads: 4,
			},
			cnf: func() *ini.Section {
				return nil
			},
		},
		"missing key keeps default": {
			expected: &configureInstanceOpts{
				applierWorkerThreads: 4,
			},
			cnf: func() *ini.Section {
				cnf := `
				[mysqld]
				group_replication_communication_stack=MYSQL
				`
				myCnfFile := io.NopCloser(bytes.NewReader([]byte(cnf)))
				myCnf, err := config.ParseSection(myCnfFile, "mysqld")
				require.NoError(t, err)
				return myCnf
			},
		},
		"empty value keeps default": {
			expected: &configureInstanceOpts{
				applierWorkerThreads: 4,
			},
			cnf: func() *ini.Section {
				cnf := `
				[mysqld]
				replica_parallel_workers=
				`
				myCnfFile := io.NopCloser(bytes.NewReader([]byte(cnf)))
				myCnf, err := config.ParseSection(myCnfFile, "mysqld")
				require.NoError(t, err)
				return myCnf
			},
		},
		"explicit value": {
			expected: &configureInstanceOpts{
				applierWorkerThreads: 8,
			},
			cnf: func() *ini.Section {
				cnf := `
				[mysqld]
				replica_parallel_workers=8
				`
				myCnfFile := io.NopCloser(bytes.NewReader([]byte(cnf)))
				myCnf, err := config.ParseSection(myCnfFile, "mysqld")
				require.NoError(t, err)
				return myCnf
			},
		},
		"explicit value with loose prefix": {
			expected: &configureInstanceOpts{
				applierWorkerThreads: 16,
			},
			cnf: func() *ini.Section {
				cnf := `
				[mysqld]
				loose_replica_parallel_workers=16
				`
				myCnfFile := io.NopCloser(bytes.NewReader([]byte(cnf)))
				myCnf, err := config.ParseSection(myCnfFile, "mysqld")
				require.NoError(t, err)
				return myCnf
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			opts, err := getConfigureInstanceOpts(tt.cnf())
			require.NoError(t, err)

			assert.Equal(t, tt.expected.applierWorkerThreads, opts.applierWorkerThreads)
		})
	}
}

func TestReadMyCnf(t *testing.T) {
	const (
		userConf = "[mysqld]\nreplica_parallel_workers=9\n"
		autoConf = "\nreplica_parallel_workers=5\n"
	)

	tests := map[string]struct {
		setup func(t *testing.T) []string

		wantWorkers string
		wantNil     bool
		wantErrMsg  string
	}{
		"neither file exists": {
			setup:   func(t *testing.T) []string { return []string{"/nonexistent/my.cnf", "/nonexistent/auto.cnf"} },
			wantNil: true,
		},
		"only the auto-config exists": {
			setup: func(t *testing.T) []string {
				dir := t.TempDir()
				return []string{dir + "/my.cnf", writeCnf(t, dir+"/auto.cnf", autoConf)}
			},
			wantWorkers: "5",
		},
		"only the user configuration exists": {
			setup: func(t *testing.T) []string {
				dir := t.TempDir()
				return []string{writeCnf(t, dir+"/my.cnf", userConf), dir + "/auto.cnf"}
			},
			wantWorkers: "9",
		},
		"the user configuration wins over the auto-config": {
			setup: func(t *testing.T) []string {
				dir := t.TempDir()
				return []string{writeCnf(t, dir+"/my.cnf", userConf), writeCnf(t, dir+"/auto.cnf", autoConf)}
			},
			wantWorkers: "9",
		},
		"an unreadable file is an error rather than a silent fallback": {
			setup: func(t *testing.T) []string {
				dir := t.TempDir()
				notADir := writeCnf(t, dir+"/my.cnf", userConf)
				return []string{notADir + "/nested.cnf", writeCnf(t, dir+"/auto.cnf", autoConf)}
			},
			wantErrMsg: "open",
		},
		"a malformed file is an error": {
			setup: func(t *testing.T) []string {
				dir := t.TempDir()
				return []string{writeCnf(t, dir+"/my.cnf", "[mysqld\nbroken")}
			},
			wantErrMsg: "failed to parse",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := readMyCnf(tc.setup(t)...)
			if tc.wantErrMsg != "" {
				require.ErrorContains(t, err, tc.wantErrMsg)
				return
			}
			require.NoError(t, err)
			if tc.wantNil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			value, err := config.GetKeyValue(got, "replica_parallel_workers")
			require.NoError(t, err)
			assert.Equal(t, tc.wantWorkers, value)
		})
	}
}

func writeCnf(t *testing.T, path, content string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}
