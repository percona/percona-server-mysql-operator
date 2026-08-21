package binlogsource

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testIndex(t *testing.T) *Index {
	t.Helper()
	idx, err := ReadIndex(filepath.Join("testdata", "binlog.index"))
	require.NoError(t, err)
	return idx
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestReadIndexResolvesRelativePaths(t *testing.T) {
	idx := testIndex(t)
	require.Len(t, idx.Files, 3)

	for _, f := range idx.Files {
		assert.False(t, !filepath.IsAbs(f) && !fileExists(f), "file is not readable")
	}
}

// The last file's PREVIOUS_GTIDS is by definition everything committed
// before it, so the total must contain it.
func TestExecutedGTIDsCoversEveryFile(t *testing.T) {
	idx := testIndex(t)
	all, err := ExecutedGTIDs(idx)
	require.NoError(t, err)

	prev, err := PreviousGTIDs(idx.Files[len(idx.Files)-1])
	require.NoError(t, err)

	assert.True(t, all.Contain(prev))
	assert.NotEmpty(t, all.String())
}

// A replica that has everything up to the last file must be served from
// the last file, not from the beginning.
func TestStartFilePicksNewestFullyCoveredFile(t *testing.T) {
	idx := testIndex(t)

	last := idx.Files[len(idx.Files)-1]
	prev, err := PreviousGTIDs(last)
	require.NoError(t, err)

	got, err := StartFile(idx, prev)
	require.NoError(t, err)

	assert.Equal(t, last, got)
}

func TestChecksumIsReadFromTheFormatDescriptionEvent(t *testing.T) {
	on, err := fileHasChecksum(filepath.Join("testdata", "binlog.000001"))
	require.NoError(t, err)
	assert.True(t, on, "binary logs written by MySQL 8 carry a CRC32 trailer")
}

func TestChecksumOfAFileThatIsNotABinaryLog(t *testing.T) {
	_, err := fileHasChecksum(filepath.Join("testdata", "binlog.index"))
	require.Error(t, err)
}

// A replica needs the file it is served from and every file after it.
func TestSinceReturnsTheRemainingFiles(t *testing.T) {
	idx := testIndex(t)

	assert.Equal(t, idx.Files, idx.Since(idx.Files[0]))
	assert.Equal(t, idx.Files[2:], idx.Since(idx.Files[2]))
	assert.Nil(t, idx.Since("binlog.999999"))
}

// truncatedCopy returns a copy of a binary log with n bytes taken off the end.
func truncatedCopy(t *testing.T, file string, n int) string {
	t.Helper()

	raw, err := os.ReadFile(file)
	require.NoError(t, err)

	copied := filepath.Join(t.TempDir(), filepath.Base(file))
	require.NoError(t, os.WriteFile(copied, raw[:len(raw)-n], 0o644))

	return copied
}

func TestCompleteLengthOfAClosedLogIsItsSize(t *testing.T) {
	file := filepath.Join("testdata", "binlog.000003")

	info, err := os.Stat(file)
	require.NoError(t, err)

	n, err := completeLength(file)
	require.NoError(t, err)
	assert.Equal(t, info.Size(), n, "every event in a closed log is whole")
}

// However much of the last event is missing, the answer is where that event
// begins: everything before it is still whole.
func TestCompleteLengthStopsWhereTheFragmentBegins(t *testing.T) {
	file := filepath.Join("testdata", "binlog.000003")

	size, err := completeLength(file)
	require.NoError(t, err)

	// One byte short is enough to make the last event a fragment.
	begins, err := completeLength(truncatedCopy(t, file, 1))
	require.NoError(t, err)
	assert.Less(t, begins, size)

	for _, missing := range []int{4, 8, replication.EventHeaderSize + 1} {
		n, err := completeLength(truncatedCopy(t, file, missing))
		require.NoError(t, err)
		assert.Equal(t, begins, n, "%d bytes short", missing)
	}
}

func TestCompleteLengthRejectsAnImpossibleEventSize(t *testing.T) {
	raw := make([]byte, binlogFileHeaderSize+replication.EventHeaderSize)
	copy(raw, replication.BinLogFileHeader)
	binary.LittleEndian.PutUint32(raw[binlogFileHeaderSize+eventSizeOffset:], 5)

	file := filepath.Join(t.TempDir(), "binlog.000001")
	require.NoError(t, os.WriteFile(file, raw, 0o644))

	_, err := completeLength(file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claims to be 5 bytes")
}

// uncommittedCopy returns a copy of a binary log whose final transaction has
// lost its commit, which is what the old primary's newest log looks like if it
// died part way through writing one.
func uncommittedCopy(t *testing.T, file string) (path string, lastCommit int64) {
	t.Helper()

	p := replication.NewBinlogParser()
	require.NoError(t, p.ParseFile(file, 0, func(e *replication.BinlogEvent) error {
		if _, ok := e.Event.(*replication.XIDEvent); ok {
			lastCommit = int64(e.Header.LogPos) - int64(e.Header.EventSize)
		}
		return nil
	}))
	require.Positive(t, lastCommit, "%s has no committed transaction to cut", file)

	raw, err := os.ReadFile(file)
	require.NoError(t, err)

	path = filepath.Join(t.TempDir(), filepath.Base(file))
	require.NoError(t, os.WriteFile(path, raw[:lastCommit], 0o644))

	return path, lastCommit
}

// Everything in a closed log is committed, including the rotate event that ends
// it -- that one is not inside a transaction and must still be served.
func TestCommittedLengthOfAClosedLogIsItsSize(t *testing.T) {
	for _, name := range []string{"binlog.000002", "binlog.000003"} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join("testdata", name)

			info, err := os.Stat(file)
			require.NoError(t, err)

			n, err := committedLength(file, true)
			require.NoError(t, err)
			assert.Equal(t, info.Size(), n)
		})
	}
}

// A transaction nothing will ever commit is left out entirely, not served up to
// its last whole event.
func TestCommittedLengthDropsAnUncommittedTail(t *testing.T) {
	file, lastCommit := uncommittedCopy(t, filepath.Join("testdata", "binlog.000003"))

	whole, err := completeLength(file)
	require.NoError(t, err)
	require.Equal(t, lastCommit, whole, "the cut should land on an event boundary")

	n, err := committedLength(file, true)
	require.NoError(t, err)
	assert.Less(t, n, whole, "the dangling transaction is still being served")

	// Everything up to the GTID event that opened it, and nothing after.
	assert.Equal(t, int64(198), n)
}
