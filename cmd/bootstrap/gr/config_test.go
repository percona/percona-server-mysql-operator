package gr

import (
	"bytes"
	"io"
	"testing"

	"github.com/go-ini/ini"
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
				myCnf, err := parseMyCnf(myCnfFile)
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
				myCnf, err := parseMyCnf(myCnfFile)
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
				myCnf, err := parseMyCnf(myCnfFile)
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
