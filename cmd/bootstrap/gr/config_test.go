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
	cnf := `
	[mysqld]
    group_replication_single_primary_mode=ON
    group_replication_paxos_single_leader=OFF
    group_replication_communication_stack=MYSQL
	`

	myCnfFile := io.NopCloser(bytes.NewReader([]byte(cnf)))
	myCnf, err := parseMyCnf(myCnfFile)
	require.NoError(t, err)

	opts, err := getCreateClusterOpts(myCnf)
	require.NoError(t, err)

	assert.Equal(t, opts.force, false)
	assert.Equal(t, opts.multiPrimary, false)
	assert.Equal(t, opts.paxosSingleLeader, false)
	assert.Equal(t, opts.communicationStack, "MYSQL")

	cnf = `
	[mysqld]
    loose_group_replication_single_primary_mode=OFF
    loose_group_replication_paxos_single_leader=OFF
    loose_group_replication_communication_stack=XCOM
	`

	myCnfFile = io.NopCloser(bytes.NewReader([]byte(cnf)))
	myCnf, err = parseMyCnf(myCnfFile)
	require.NoError(t, err)

	opts, err = getCreateClusterOpts(myCnf)
	require.NoError(t, err)

	assert.Equal(t, opts.force, true)
	assert.Equal(t, opts.multiPrimary, true)
	assert.Equal(t, opts.paxosSingleLeader, false)
	assert.Equal(t, opts.communicationStack, "XCOM")

	cnf = `
    group_replication_single_primary_mode=ON
    group_replication_paxos_single_leader=ON
    group_replication_communication_stack=XCOM
	`

	myCnfFile = io.NopCloser(bytes.NewReader([]byte(cnf)))
	myCnf, err = parseMyCnf(myCnfFile)
	require.NoError(t, err)

	opts, err = getCreateClusterOpts(myCnf)
	require.NoError(t, err)

	assert.Equal(t, opts.force, false)
	assert.Equal(t, opts.multiPrimary, false)
	assert.Equal(t, opts.paxosSingleLeader, true)
	assert.Equal(t, opts.communicationStack, "XCOM")

	var nonExistentCnf *ini.Section
	opts, err = getCreateClusterOpts(nonExistentCnf)
	require.NoError(t, err)

	assert.Equal(t, opts.force, false)
	assert.Equal(t, opts.multiPrimary, false)
	assert.Equal(t, opts.paxosSingleLeader, true)
	assert.Equal(t, opts.communicationStack, "MYSQL")
}
