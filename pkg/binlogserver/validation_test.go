package binlogserver

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	storagefake "github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage/fake"
)

const testUUID = "3E11FA47-71CA-11E1-9E33-C80AA9429562"

type fakeArchive struct {
	storagefake.FakeStorageClient
	objects map[string]string
	reads   []string
	onRead  func(string)
}

func (a *fakeArchive) GetObject(_ context.Context, name string) (io.ReadCloser, error) {
	a.reads = append(a.reads, name)
	if a.onRead != nil {
		a.onRead(name)
	}
	content, ok := a.objects[name]
	if !ok {
		return nil, errors.Errorf("object %s not found", name)
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func testArchive() *fakeArchive {
	return &fakeArchive{objects: map[string]string{
		binlogIndexName: "./binlog.000001\n./binlog.000002\n",
		"binlog.000001.json": `{
			"min_timestamp": "2026-09-09T12:00:00",
			"max_timestamp": "2026-09-09T12:30:00",
			"previous_gtids": "3E11FA47-71CA-11E1-9E33-C80AA9429562:1-10",
			"added_gtids": "3E11FA47-71CA-11E1-9E33-C80AA9429562:11-20"
		}`,
		"binlog.000002.json": `{
			"min_timestamp": "2026-09-09T12:30:00",
			"max_timestamp": "2026-09-09T13:00:00",
			"previous_gtids": "3E11FA47-71CA-11E1-9E33-C80AA9429562:1-20",
			"added_gtids": "3E11FA47-71CA-11E1-9E33-C80AA9429562:21-30"
		}`,
	}}
}

func dateRestore(date string) *apiv1.PerconaServerMySQLRestore {
	return &apiv1.PerconaServerMySQLRestore{Spec: apiv1.PerconaServerMySQLRestoreSpec{
		PITR: &apiv1.RestorePITRSpec{Type: apiv1.PITRDate, Date: date},
	}}
}

func gtidRestore(value string) *apiv1.PerconaServerMySQLRestore {
	return &apiv1.PerconaServerMySQLRestore{Spec: apiv1.PerconaServerMySQLRestoreSpec{
		PITR: &apiv1.RestorePITRSpec{Type: apiv1.PITRGtid, GTID: value},
	}}
}

func TestValidateTargetTimestamp(t *testing.T) {
	tests := map[string]struct {
		target      string
		notCovered  bool
		expectedErr string
	}{
		"inside archive":        {target: "2026-09-09 12:45:00"},
		"UTC date":              {target: "2026-09-09T12:45:00Z"},
		"RFC3339 offset":        {target: "2026-09-09T14:45:00.25+02:00"},
		"offset before archive": {target: "2026-09-09T13:59:59+02:00", notCovered: true, expectedErr: "archive starts at 2026-09-09T12:00:00"},
		"at archive start":      {target: "2026-09-09 12:00:00"},
		"past archive":          {target: "2030-01-01 00:00:00"},
		"before archive":        {target: "2026-09-09 11:00:00", notCovered: true, expectedErr: "archive starts at 2026-09-09T12:00:00"},
		"malformed timestamp":   {target: "yesterday", expectedErr: "invalid pitr target"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			archive := testArchive()
			err := ValidateTarget(t.Context(), archive, dateRestore(tt.target))
			assert.Equal(t, tt.notCovered, errors.Is(err, ErrTargetNotCovered))
			if tt.expectedErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.expectedErr)
			}
			if name != "malformed timestamp" {
				assert.Equal(t, []string{binlogIndexName, "binlog.000001.json"}, archive.reads)
			}
		})
	}
}

func TestValidateTargetGTIDChecksOnlyStableLowerBound(t *testing.T) {
	tests := map[string]struct {
		target      string
		notCovered  bool
		invalid     bool
		expectedErr string
	}{
		"inside observed archive": {target: testUUID + ":11-25"},
		"tagged target":           {target: testUUID + ":blue:11-25"},
		"past observed tail":      {target: testUUID + ":11-40"},
		"unknown source":          {target: "11111111-1111-1111-1111-111111111111:1-5"},
		"predates archive":        {target: testUUID + ":5-15", notCovered: true, expectedErr: "missing 3e11fa47-71ca-11e1-9e33-c80aa9429562:5-10"},
		"empty":                   {target: "", invalid: true, expectedErr: "GTID set is empty"},
		"invalid UUID":            {target: "not-a-uuid:1-5", invalid: true, expectedErr: "malformed GTID source"},
		"transaction zero":        {target: testUUID + ":0", invalid: true, expectedErr: "must be positive"},
		"multiple tags in entry":  {target: testUUID + ":blue:11-15:green:16-20"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			archive := testArchive()
			err := ValidateTarget(t.Context(), archive, gtidRestore(tt.target))
			assert.Equal(t, tt.notCovered, errors.Is(err, ErrTargetNotCovered))
			assert.Equal(t, tt.invalid, errors.Is(err, ErrInvalidTarget))
			if tt.expectedErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.expectedErr)
			}
		})
	}
}

func TestValidateTargetRejectsTaggedGTIDBeforeArchive(t *testing.T) {
	archive := testArchive()
	archive.objects["binlog.000001.json"] = `{
		"previous_gtids":"` + testUUID + `:domain_1:1-10",
		"added_gtids":"` + testUUID + `:domain_1:11-20"
	}`

	err := ValidateTarget(t.Context(), archive, gtidRestore(testUUID+":Domain_1:5-15"))
	assert.ErrorIs(t, err, ErrTargetNotCovered)
	assert.Contains(t, err.Error(), "domain_1:5-10")
}

func TestValidateTargetDoesNotRejectMovingTail(t *testing.T) {
	archive := testArchive()
	archive.objects[binlogIndexName] += "./binlog.000003\n"
	archive.objects["binlog.000003.json"] = `{
		"previous_gtids":"` + testUUID + `:1-30",
		"added_gtids":"` + testUUID + `:31-35",
		"min_timestamp":"2026-09-09T13:00:00"
	}`

	err := ValidateTarget(t.Context(), archive, gtidRestore(testUUID+":11-40"))
	require.NoError(t, err)
	assert.Equal(t, []string{binlogIndexName, "binlog.000001.json"}, archive.reads)
}

func TestValidateTargetArchiveProblemsDoNotRejectTarget(t *testing.T) {
	tests := map[string]func(*fakeArchive){
		"empty archive":    func(a *fakeArchive) { a.objects[binlogIndexName] = "" },
		"unreadable index": func(a *fakeArchive) { delete(a.objects, binlogIndexName) },
		"missing GTID field": func(a *fakeArchive) {
			a.objects["binlog.000001.json"] = `{"previous_gtids":""}`
			a.objects[binlogIndexName] = "./binlog.000001\n"
		},
		"position metadata": func(a *fakeArchive) {
			a.objects["binlog.000001.json"] = `{"min_timestamp":"2026-09-09T12:00:00"}`
			a.objects[binlogIndexName] = "./binlog.000001\n"
		},
		"trailing JSON": func(a *fakeArchive) {
			a.objects["binlog.000001.json"] += ` {}`
			a.objects[binlogIndexName] = "./binlog.000001\n"
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			archive := testArchive()
			mutate(archive)
			err := ValidateTarget(t.Context(), archive, gtidRestore(testUUID+":11-20"))
			assert.NotErrorIs(t, err, ErrTargetNotCovered)
			assert.NotErrorIs(t, err, ErrInvalidTarget)
		})
	}
}

func TestBinlogMetadataAcceptsLargeObject(t *testing.T) {
	archive := testArchive()
	archive.objects["binlog.000001.json"] = `{"padding":"` + strings.Repeat("x", 1<<20) + `"}`

	_, err := getBinlogMetadata(t.Context(), archive, "binlog.000001")
	require.NoError(t, err)
}

func TestValidateTargetDoesNotRejectAfterInvalidEarlierMetadata(t *testing.T) {
	archive := testArchive()
	archive.objects["binlog.000001.json"] = "not-json"

	err := ValidateTarget(t.Context(), archive, dateRestore("2026-09-09 12:15:00"))
	require.ErrorContains(t, err, "binlog.000001")
	assert.NotErrorIs(t, err, ErrTargetNotCovered)
	assert.Equal(t, []string{binlogIndexName, "binlog.000001.json"}, archive.reads)
}

func TestValidateGTIDDoesNotRejectAfterUnreadableEarlierMetadata(t *testing.T) {
	archive := testArchive()
	delete(archive.objects, "binlog.000001.json")

	err := ValidateTarget(t.Context(), archive, gtidRestore(testUUID+":11-15"))
	require.ErrorContains(t, err, "binlog.000001")
	assert.NotErrorIs(t, err, ErrTargetNotCovered)
	assert.Equal(t, []string{binlogIndexName, "binlog.000001.json"}, archive.reads)
}

func TestValidateTargetIndexValidation(t *testing.T) {
	tests := map[string]string{
		"missing dot path": "binlog.000001\n",
		"nested path":      "./nested/binlog.000001\n",
		"whitespace":       " ./binlog.000001 \n",
		"empty name":       "./\n",
	}
	for name, index := range tests {
		t.Run(name, func(t *testing.T) {
			archive := testArchive()
			archive.objects[binlogIndexName] = index
			err := ValidateTarget(t.Context(), archive, dateRestore("2026-09-09 12:15:00"))
			require.Error(t, err)
			assert.NotErrorIs(t, err, ErrTargetNotCovered)
		})
	}
}

func TestValidateTargetHandlesArchiveChangingBetweenReads(t *testing.T) {
	archive := testArchive()
	archive.onRead = func(name string) {
		if name == "binlog.000001.json" {
			archive.objects[binlogIndexName] += "./binlog.000003\n"
			archive.objects["binlog.000003.json"] = archive.objects["binlog.000002.json"]
		}
	}

	err := ValidateTarget(t.Context(), archive, gtidRestore(testUUID+":11-40"))
	require.NoError(t, err)
	assert.Equal(t, []string{binlogIndexName, "binlog.000001.json"}, archive.reads)
}

func TestValidateTargetReadsOnlyOldestValidMetadata(t *testing.T) {
	archive := testArchive()
	index := make([]string, 0, 1000)
	for i := range 1000 {
		name := fmt.Sprintf("binlog.%06d", i+1)
		index = append(index, "./"+name)
		archive.objects[name+".json"] = archive.objects["binlog.000002.json"]
	}
	index[1] = "not-a-binlog"
	archive.objects[binlogIndexName] = strings.Join(index, "\n")
	archive.objects["binlog.000001.json"] = testArchive().objects["binlog.000001.json"]

	err := ValidateTarget(t.Context(), archive, dateRestore("2026-09-09 12:45:00"))
	require.NoError(t, err)
	assert.Equal(t, []string{binlogIndexName, "binlog.000001.json"}, archive.reads)
}
