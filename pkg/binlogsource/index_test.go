package binlogsource

import (
	"os"
	"path/filepath"
	"testing"

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
