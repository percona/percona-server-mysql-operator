package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

const magic = "\xfebin"

// Long enough that no healthy test server ever hits it.
const testFetchTimeout = time.Minute

func withMagic(payload string) []byte {
	return append([]byte(magic), payload...)
}

// binlogEvent wraps payload in an event header. Only the declared length matters
// to the splice, so the rest of the header stays zeroed.
func binlogEvent(payload string) string {
	e := make([]byte, eventHeaderLen+len(payload))
	binary.LittleEndian.PutUint32(e[9:13], uint32(len(e)))
	copy(e[eventHeaderLen:], payload)

	return string(e)
}

func binlogFile(t *testing.T, dir, name, payload string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, withMagic(payload), 0o644))

	return path
}

func writeFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, content, 0o644))

	return path
}

// markStaged leaves dir the way an earlier run of the job would have left it.
func markStaged(t *testing.T, dir string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(dir, 0o755))
	writeFile(t, dir, stagingMarker, nil)
}

// stagedNames lists what dir holds besides the marker the job keeps there.
func stagedNames(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == stagingMarker {
			continue
		}
		names = append(names, entry.Name())
	}

	return names
}

func writeIndex(t *testing.T, path string, entries ...string) {
	t.Helper()

	content := ""
	if len(entries) > 0 {
		content = strings.Join(entries, "\n") + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestBinlogMagic(t *testing.T) {
	assert.Equal(t, []byte(magic), binlogMagic)
}

func TestReadRelayIndex(t *testing.T) {
	tests := []struct {
		name     string
		entries  []string
		expected []string
	}{
		{
			name:     "every entry in index order",
			entries:  []string{"relay-bin.000001", "relay-bin.000002", "relay-bin.000003"},
			expected: []string{"relay-bin.000001", "relay-bin.000002", "relay-bin.000003"},
		},
		{
			name:     "entries carry a directory prefix",
			entries:  []string{"./relay-bin.000001", "/other/dir/relay-bin.000002"},
			expected: []string{"relay-bin.000001", "relay-bin.000002"},
		},
		{
			name:     "blank lines are skipped, not counted",
			entries:  []string{"relay-bin.000001", "", "  ", "relay-bin.000002", ""},
			expected: []string{"relay-bin.000001", "relay-bin.000002"},
		},
		{
			name:     "CRLF line endings",
			entries:  []string{"relay-bin.000001\r", "relay-bin.000002\r"},
			expected: []string{"relay-bin.000001", "relay-bin.000002"},
		},
		{
			name:     "leading whitespace is trimmed",
			entries:  []string{"\trelay-bin.000001", "  relay-bin.000002"},
			expected: []string{"relay-bin.000001", "relay-bin.000002"},
		},
		{
			name:     "several basenames in one index",
			entries:  []string{"old-relay.000009", "new-relay.000001", "new-relay.000002"},
			expected: []string{"old-relay.000009", "new-relay.000001", "new-relay.000002"},
		},
		{
			name:     "an empty index",
			entries:  nil,
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index := filepath.Join(t.TempDir(), "relay-bin.index")
			writeIndex(t, index, tt.entries...)
			relayDir := t.TempDir()

			logs, err := readRelayIndex(index, relayDir)

			require.NoError(t, err)
			want := make(relayIndex, 0, len(tt.expected))
			for _, name := range tt.expected {
				want = append(want, filepath.Join(relayDir, name))
			}
			assert.Equal(t, want, logs)
		})
	}

	t.Run("index is missing", func(t *testing.T) {
		_, err := readRelayIndex(filepath.Join(t.TempDir(), "relay-bin.index"), t.TempDir())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "open ")
		assert.Contains(t, err.Error(), "no such file")
	})

	t.Run("index is a directory", func(t *testing.T) {
		_, err := readRelayIndex(t.TempDir(), t.TempDir())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read ")
	})
}

func TestRelayIndexLastClosed(t *testing.T) {
	tests := []struct {
		name     string
		logs     relayIndex
		expected string
		wantErr  string
	}{
		{
			name:     "three entries returns the second to last",
			logs:     relayIndex{"relay-bin.000001", "relay-bin.000002", "relay-bin.000003"},
			expected: "relay-bin.000002",
		},
		{
			name:     "exactly two entries returns the first",
			logs:     relayIndex{"relay-bin.000001", "relay-bin.000002"},
			expected: "relay-bin.000001",
		},
		{
			name:     "the newest basename wins over the higher suffix",
			logs:     relayIndex{"old-relay.000009", "new-relay.000001", "new-relay.000002"},
			expected: "new-relay.000001",
		},
		{
			name:    "a single entry has no closed relay log",
			logs:    relayIndex{"relay-bin.000001"},
			wantErr: "lists 1 relay log(s), so no relay log is closed",
		},
		{
			name:    "an empty index",
			logs:    relayIndex{},
			wantErr: "lists 0 relay log(s)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.logs.lastClosed()

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestRelayIndexPlace(t *testing.T) {
	// The suffixes are deliberately out of order: only the index orders the files.
	logs := relayIndex{"/var/lib/mysql/old-relay.000009", "/var/lib/mysql/new-relay.000001"}

	tests := []struct {
		name     string
		relayLog string
		expected int
	}{
		{name: "bare name", relayLog: "old-relay.000009", expected: 0},
		{name: "a later basename with a lower suffix", relayLog: "new-relay.000001", expected: 1},
		{name: "full path", relayLog: "/var/lib/mysql/new-relay.000001", expected: 1},
		{name: "another directory, same file name", relayLog: "/elsewhere/new-relay.000001", expected: 1},
		{name: "surrounding whitespace", relayLog: "  old-relay.000009 ", expected: 0},
		{name: "not listed", relayLog: "relay-bin.000001", expected: -1},
		{name: "unparseable name", relayLog: "relay-bin", expected: -1},
		{name: "empty", relayLog: "", expected: -1},
		{name: "blank", relayLog: "   ", expected: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, logs.place(tt.relayLog))
		})
	}

	t.Run("an empty index lists nothing", func(t *testing.T) {
		assert.Equal(t, -1, relayIndex{}.place("relay-bin.000001"))
	})
}

func TestAppendLog(t *testing.T) {
	const seed = "SEED"

	tests := []struct {
		name        string
		content     []byte
		stripMagic  bool
		expectedN   int64
		expectedDst string
		wantErr     string
	}{
		{
			name:        "copies the whole file",
			content:     withMagic("payload"),
			stripMagic:  false,
			expectedN:   11,
			expectedDst: seed + magic + "payload",
		},
		{
			name:        "strips the magic",
			content:     withMagic("payload"),
			stripMagic:  true,
			expectedN:   7,
			expectedDst: seed + "payload",
		},
		{
			name:        "no magic and no strip",
			content:     []byte("BBBB"),
			stripMagic:  false,
			expectedN:   4,
			expectedDst: seed + "BBBB",
		},
		{
			name:        "magic-only file appends nothing",
			content:     []byte(magic),
			stripMagic:  true,
			expectedN:   0,
			expectedDst: seed,
		},
		{
			name:        "empty file without strip",
			content:     []byte{},
			stripMagic:  false,
			expectedN:   0,
			expectedDst: seed,
		},
		{
			name:       "wrong magic",
			content:    []byte("HELOjunk"),
			stripMagic: true,
			wantErr:    "unexpected magic number",
		},
		{
			name:       "uppercase magic",
			content:    []byte("\xfeBINjunk"),
			stripMagic: true,
			wantErr:    "unexpected magic number",
		},
		{
			name:       "three bytes",
			content:    []byte("\xfebi"),
			stripMagic: true,
			wantErr:    "read magic number",
		},
		{
			name:       "two bytes",
			content:    []byte("\xfeb"),
			stripMagic: true,
			wantErr:    "read magic number",
		},
		{
			name:       "empty file with strip",
			content:    []byte{},
			stripMagic: true,
			wantErr:    "read magic number",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := writeFile(t, t.TempDir(), "binlog.000004", tt.content)
			dst := bytes.NewBufferString(seed)

			n, err := appendLog(dst, src, tt.stripMagic)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Equal(t, seed, dst.String(), "nothing may reach dst on error")
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedN, n)
			assert.Equal(t, tt.expectedDst, dst.String())
		})
	}

	t.Run("source is missing", func(t *testing.T) {
		dst := bytes.NewBufferString(seed)

		_, err := appendLog(dst, filepath.Join(t.TempDir(), "binlog.000004"), false)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such file")
		assert.Equal(t, seed, dst.String())
	})

	t.Run("copies a large file byte-exact", func(t *testing.T) {
		payload := make([]byte, 1<<20)
		_, err := rand.Read(payload)
		require.NoError(t, err)

		src := writeFile(t, t.TempDir(), "binlog.000004", append([]byte(magic), payload...))
		dst := &bytes.Buffer{}

		n, err := appendLog(dst, src, true)

		require.NoError(t, err)
		assert.Equal(t, int64(len(payload)), n)
		assert.Equal(t, payload, dst.Bytes())
	})
}

type relayLayout struct {
	dir      string
	basename string
	index    string
	target   string
}

const relayIndexContent = "./relay-bin.000001\n./relay-bin.000002\n./relay-bin.000003\n"

func newRelayLayout(t *testing.T) relayLayout {
	t.Helper()

	dir := t.TempDir()
	l := relayLayout{
		dir:      dir,
		basename: filepath.Join(dir, "relay-bin"),
		index:    filepath.Join(dir, "relay-bin.index"),
		target:   filepath.Join(dir, "relay-bin.000002"),
	}

	binlogFile(t, dir, "relay-bin.000001", "relay-one")
	binlogFile(t, dir, "relay-bin.000002", "relay-two")
	binlogFile(t, dir, "relay-bin.000003", "")
	require.NoError(t, os.WriteFile(l.index, []byte(relayIndexContent), 0o644))

	return l
}

func (l relayLayout) logs(t *testing.T) relayIndex {
	t.Helper()

	logs, err := readRelayIndex(l.index, l.dir)
	require.NoError(t, err)

	return logs
}

func newSourceLogs(t *testing.T) []string {
	t.Helper()

	dir := t.TempDir()

	return []string{
		writeFile(t, dir, "binlog.000004", []byte("tail-of-four")),
		binlogFile(t, dir, "binlog.000005", "whole-five"),
		binlogFile(t, dir, "binlog.000006", "whole-six"),
	}
}

func (l relayLayout) assertUntouched(t *testing.T) {
	t.Helper()

	active, err := os.ReadFile(filepath.Join(l.dir, "relay-bin.000003"))
	require.NoError(t, err)
	assert.Equal(t, []byte(magic), active, "the active relay log must not change")

	index, err := os.ReadFile(l.index)
	require.NoError(t, err)
	assert.Equal(t, relayIndexContent, string(index), "the index must not change")
}

func TestUpdateRelayLogs(t *testing.T) {
	t.Run("appends every source log into the newest closed relay log", func(t *testing.T) {
		l := newRelayLayout(t)
		before, err := os.ReadFile(l.target)
		require.NoError(t, err)

		target, startPos, err := updateRelayLogs(newSourceLogs(t), "relay-bin.000002", l.logs(t))

		require.NoError(t, err)
		assert.Equal(t, l.target, target)
		assert.Equal(t, uint64(len(before)), startPos)

		after, err := os.ReadFile(l.target)
		require.NoError(t, err)
		assert.Equal(t, string(before)+"tail-of-four"+"whole-five"+"whole-six", string(after))

		assert.Equal(t, 1, bytes.Count(after, binlogMagic))
		assert.True(t, bytes.HasPrefix(after, binlogMagic))

		l.assertUntouched(t)
	})

	t.Run("an applier behind the target is fine", func(t *testing.T) {
		l := newRelayLayout(t)

		target, startPos, err := updateRelayLogs(newSourceLogs(t), "relay-bin.000001", l.logs(t))

		require.NoError(t, err)
		assert.Equal(t, l.target, target)
		assert.Equal(t, uint64(13), startPos)
		l.assertUntouched(t)
	})

	t.Run("refuses an applier ahead of the newest closed relay log", func(t *testing.T) {
		l := newRelayLayout(t)
		before, err := os.ReadFile(l.target)
		require.NoError(t, err)

		_, _, err = updateRelayLogs(newSourceLogs(t), "relay-bin.000003", l.logs(t))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "applier is on relay-bin.000003, ahead of the newest closed relay log relay-bin.000002")

		after, err := os.ReadFile(l.target)
		require.NoError(t, err)
		assert.Equal(t, before, after, "the target must never be opened for write")
		l.assertUntouched(t)
	})

	// mysqld keeps rotating through the same index after the relay log basename
	// changes, so a lower suffix can sit after a higher one.
	t.Run("a lower suffix after a higher one is still ahead", func(t *testing.T) {
		l := newRelayLayout(t)
		binlogFile(t, l.dir, "new-relay.000001", "")
		writeIndex(t, l.index, "./relay-bin.000001", "./relay-bin.000002", "./relay-bin.000003", "./new-relay.000001")

		_, _, err := updateRelayLogs(newSourceLogs(t), "new-relay.000001", l.logs(t))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "applier is on new-relay.000001, ahead of the newest closed relay log relay-bin.000003")

		after, err := os.ReadFile(filepath.Join(l.dir, "relay-bin.000003"))
		require.NoError(t, err)
		assert.Equal(t, []byte(magic), after, "the target must never be opened for write")
	})

	t.Run("a higher suffix before a lower one is still behind", func(t *testing.T) {
		l := newRelayLayout(t)
		target := binlogFile(t, l.dir, "new-relay.000001", "new-one")
		binlogFile(t, l.dir, "new-relay.000002", "")
		writeIndex(t, l.index, "./relay-bin.000009", "./new-relay.000001", "./new-relay.000002")

		got, startPos, err := updateRelayLogs(newSourceLogs(t), "relay-bin.000009", l.logs(t))

		require.NoError(t, err)
		assert.Equal(t, target, got)
		assert.Equal(t, uint64(len(magic+"new-one")), startPos)

		after, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, magic+"new-one"+"tail-of-four"+"whole-five"+"whole-six", string(after))
	})

	t.Run("only one relay log in the index", func(t *testing.T) {
		l := newRelayLayout(t)
		writeIndex(t, l.index, "./relay-bin.000001")

		_, _, err := updateRelayLogs(newSourceLogs(t), "relay-bin.000001", l.logs(t))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "get relay log to append to")
		assert.Contains(t, err.Error(), "so no relay log is closed")
	})

	t.Run("target listed in the index but missing on disk", func(t *testing.T) {
		l := newRelayLayout(t)
		require.NoError(t, os.Remove(l.target))

		_, _, err := updateRelayLogs(newSourceLogs(t), "relay-bin.000002", l.logs(t))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "stat ")
		assert.Contains(t, err.Error(), "no such file")
	})

	t.Run("target is a directory", func(t *testing.T) {
		l := newRelayLayout(t)
		require.NoError(t, os.Remove(l.target))
		require.NoError(t, os.Mkdir(l.target, 0o755))

		_, _, err := updateRelayLogs(newSourceLogs(t), "relay-bin.000002", l.logs(t))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "open ")
	})

	t.Run("an applier relay log the index does not list", func(t *testing.T) {
		l := newRelayLayout(t)

		_, _, err := updateRelayLogs(newSourceLogs(t), "relay-bin", l.logs(t))

		require.Error(t, err)
		assert.Contains(t, err.Error(), `the index does not list the applier's relay log "relay-bin"`)
		l.assertUntouched(t)
	})

	t.Run("uninitialized applier", func(t *testing.T) {
		l := newRelayLayout(t)

		_, _, err := updateRelayLogs(newSourceLogs(t), "", l.logs(t))

		require.Error(t, err)
		assert.Contains(t, err.Error(), `the index does not list the applier's relay log ""`)
	})

	t.Run("a later source log with a bad magic writes nothing", func(t *testing.T) {
		l := newRelayLayout(t)
		before, err := os.ReadFile(l.target)
		require.NoError(t, err)

		srcDir := t.TempDir()
		sourceLogs := []string{
			writeFile(t, srcDir, "binlog.000004", []byte("tail-of-four")),
			writeFile(t, srcDir, "binlog.000005", []byte("NOPEwhole-five")),
		}

		_, _, err = updateRelayLogs(sourceLogs, "relay-bin.000002", l.logs(t))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected magic number")

		after, err := os.ReadFile(l.target)
		require.NoError(t, err)
		assert.Equal(t, before, after, "the splice is rejected before the target is opened for write")
		l.assertUntouched(t)
	})

	// Rolling a bad splice back is the safety net; rejecting it up front is the
	// point. An unwritable target proves the source logs are checked before it
	// is ever opened.
	t.Run("a bad magic is caught before the target is opened", func(t *testing.T) {
		l := newRelayLayout(t)
		require.NoError(t, os.Remove(l.target))
		require.NoError(t, os.Mkdir(l.target, 0o755))

		srcDir := t.TempDir()
		sourceLogs := []string{
			writeFile(t, srcDir, "binlog.000004", []byte("tail-of-four")),
			writeFile(t, srcDir, "binlog.000005", []byte("NOPEwhole-five")),
		}

		_, _, err := updateRelayLogs(sourceLogs, "relay-bin.000002", l.logs(t))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected magic number")
		assert.NotContains(t, err.Error(), "open ")
	})

	t.Run("a missing source log", func(t *testing.T) {
		l := newRelayLayout(t)
		before, err := os.ReadFile(l.target)
		require.NoError(t, err)

		_, _, err = updateRelayLogs([]string{filepath.Join(t.TempDir(), "binlog.000004")},
			"relay-bin.000002", l.logs(t))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such file")

		after, err := os.ReadFile(l.target)
		require.NoError(t, err)
		assert.Equal(t, before, after)
	})

	t.Run("nothing to splice leaves the relay logs alone", func(t *testing.T) {
		l := newRelayLayout(t)
		before, err := os.ReadFile(l.target)
		require.NoError(t, err)
		empty := writeFile(t, t.TempDir(), "binlog.000004", nil)

		target, startPos, err := updateRelayLogs([]string{empty}, "relay-bin.000002", l.logs(t))

		require.NoError(t, err)
		assert.Equal(t, "relay-bin.000002", target)
		assert.Equal(t, uint64(0), startPos)

		after, err := os.ReadFile(l.target)
		require.NoError(t, err)
		assert.Equal(t, before, after)
		l.assertUntouched(t)
	})

	t.Run("nothing to splice with only one relay log in the index", func(t *testing.T) {
		l := newRelayLayout(t)
		writeIndex(t, l.index, "./relay-bin.000003")
		empty := writeFile(t, t.TempDir(), "binlog.000004", nil)

		target, startPos, err := updateRelayLogs([]string{empty}, "relay-bin.000003", l.logs(t))

		require.NoError(t, err)
		assert.Equal(t, "relay-bin.000003", target)
		assert.Equal(t, uint64(0), startPos)
	})

	t.Run("no source logs", func(t *testing.T) {
		l := newRelayLayout(t)
		writeIndex(t, l.index, "./relay-bin.000003")

		target, startPos, err := updateRelayLogs(nil, "relay-bin.000003", l.logs(t))

		require.NoError(t, err)
		assert.Equal(t, "relay-bin.000003", target)
		assert.Equal(t, uint64(0), startPos)
	})

	t.Run("nothing to splice tolerates an uninitialized applier", func(t *testing.T) {
		l := newRelayLayout(t)

		target, startPos, err := updateRelayLogs(nil, "", l.logs(t))

		require.NoError(t, err)
		assert.Empty(t, target)
		assert.Equal(t, uint64(0), startPos)
	})

	t.Run("precedes every source log after the first with a Rotate event", func(t *testing.T) {
		l := newRelayLayout(t)
		before, err := os.ReadFile(l.target)
		require.NoError(t, err)

		dir := t.TempDir()
		h := sourceHeader{serverID: 42, checksummed: true}
		// The first log is a tail cut at a position, so it carries neither a magic
		// number nor a format description event. Every later one is a whole file.
		first := writeFile(t, dir, "binlog.000004", []byte("tail-of-four"))
		second := writeFile(t, dir, "binlog.000005", append([]byte(magic), append(fde(42, true), []byte("whole-five")...)...))

		_, _, err = updateRelayLogs([]string{first, second}, "relay-bin.000002", l.logs(t))
		require.NoError(t, err)

		after, err := os.ReadFile(l.target)
		require.NoError(t, err)
		want := string(before) + "tail-of-four" +
			string(rotateEvent("binlog.000005", h)) + string(fde(42, true)) + "whole-five"
		assert.Equal(t, want, string(after))

		assert.Equal(t, 1, bytes.Count(after, binlogMagic), "only the relay log's own magic number may remain")
		l.assertUntouched(t)
	})

	t.Run("a source log with no format description event is appended without a Rotate event", func(t *testing.T) {
		l := newRelayLayout(t)
		before, err := os.ReadFile(l.target)
		require.NoError(t, err)

		_, _, err = updateRelayLogs(newSourceLogs(t), "relay-bin.000002", l.logs(t))
		require.NoError(t, err)

		after, err := os.ReadFile(l.target)
		require.NoError(t, err)
		assert.Equal(t, string(before)+"tail-of-four"+"whole-five"+"whole-six", string(after),
			"a splice that cannot name the source's files must still land")
	})
}

func TestValidateSourceLogs(t *testing.T) {
	dir := t.TempDir()

	t.Run("the cut first log needs no magic", func(t *testing.T) {
		require.NoError(t, validateSourceLogs([]string{
			writeFile(t, dir, "cut.000004", []byte("tail-of-four")),
			binlogFile(t, dir, "cut.000005", "whole-five"),
		}))
	})

	t.Run("a later log without a magic is rejected", func(t *testing.T) {
		err := validateSourceLogs([]string{
			writeFile(t, dir, "bad.000004", []byte("tail-of-four")),
			writeFile(t, dir, "bad.000005", []byte("NOPEwhole-five")),
			binlogFile(t, dir, "bad.000006", "whole-six"),
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "bad.000005")
		assert.Contains(t, err.Error(), "unexpected magic number")
	})

	t.Run("a later log too short to hold a magic is rejected", func(t *testing.T) {
		err := validateSourceLogs([]string{
			writeFile(t, dir, "short.000004", []byte("tail-of-four")),
			writeFile(t, dir, "short.000005", []byte("\xfeb")),
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read magic number")
	})

	t.Run("no source logs", func(t *testing.T) {
		require.NoError(t, validateSourceLogs(nil))
	})

	t.Run("a missing later log is rejected", func(t *testing.T) {
		err := validateSourceLogs([]string{
			writeFile(t, dir, "gone.000004", []byte("tail-of-four")),
			filepath.Join(dir, "gone.000005"),
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such file")
	})
}

func TestSpliceInto(t *testing.T) {
	t.Run("appends every source log", func(t *testing.T) {
		l := newRelayLayout(t)
		before, err := os.ReadFile(l.target)
		require.NoError(t, err)

		require.NoError(t, spliceInto(l.target, uint64(len(before)), newSourceLogs(t)))

		after, err := os.ReadFile(l.target)
		require.NoError(t, err)
		assert.Equal(t, string(before)+"tail-of-four"+"whole-five"+"whole-six", string(after))
	})

	// A splice that dies partway leaves a truncated event behind, and mysqld
	// never gets the SQL thread past it. Whatever went in has to come back out.
	t.Run("rolls the relay log back when a later source log fails", func(t *testing.T) {
		l := newRelayLayout(t)
		before, err := os.ReadFile(l.target)
		require.NoError(t, err)

		srcDir := t.TempDir()
		sourceLogs := []string{
			writeFile(t, srcDir, "binlog.000004", []byte("tail-of-four")),
			filepath.Join(srcDir, "binlog.000005"),
			binlogFile(t, srcDir, "binlog.000006", "whole-six"),
		}

		err = spliceInto(l.target, uint64(len(before)), sourceLogs)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such file")

		after, err := os.ReadFile(l.target)
		require.NoError(t, err)
		assert.Equal(t, before, after, "the bytes from the first source log must be gone")
		l.assertUntouched(t)
	})

	t.Run("rolls back to the recorded length, not to empty", func(t *testing.T) {
		l := newRelayLayout(t)
		before, err := os.ReadFile(l.target)
		require.NoError(t, err)
		require.NotEmpty(t, before)

		err = spliceInto(l.target, uint64(len(before)), []string{filepath.Join(t.TempDir(), "binlog.000004")})

		require.Error(t, err)

		after, err := os.ReadFile(l.target)
		require.NoError(t, err)
		assert.Equal(t, before, after)
	})
}

func TestPendingBytes(t *testing.T) {
	t.Run("sums the staged logs", func(t *testing.T) {
		dir := t.TempDir()
		logs := []string{
			writeFile(t, dir, "binlog.000004", []byte("tail")),
			binlogFile(t, dir, "binlog.000005", "whole-five"),
		}

		total, err := pendingBytes(logs)

		require.NoError(t, err)
		assert.Equal(t, uint64(4+len(magic+"whole-five")), total)
	})

	t.Run("a single empty log means the replica is caught up", func(t *testing.T) {
		empty := writeFile(t, t.TempDir(), "binlog.000004", nil)

		total, err := pendingBytes([]string{empty})

		require.NoError(t, err)
		assert.Equal(t, uint64(0), total)
	})

	t.Run("no logs", func(t *testing.T) {
		total, err := pendingBytes(nil)

		require.NoError(t, err)
		assert.Equal(t, uint64(0), total)
	})

	t.Run("a missing log", func(t *testing.T) {
		_, err := pendingBytes([]string{filepath.Join(t.TempDir(), "binlog.000004")})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "stat ")
		assert.Contains(t, err.Error(), "no such file")
	})
}

type tarEntry struct {
	name     string
	content  string
	typeflag byte
}

func tarBytes(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}

		hdr := &tar.Header{Name: e.name, Mode: 0o644, Typeflag: typeflag}
		switch typeflag {
		case tar.TypeReg:
			hdr.Size = int64(len(e.content))
		case tar.TypeSymlink:
			hdr.Linkname = e.content
		}

		require.NoError(t, tw.WriteHeader(hdr))
		if typeflag == tar.TypeReg {
			_, err := tw.Write([]byte(e.content))
			require.NoError(t, err)
		}
	}

	require.NoError(t, tw.Close())

	return buf.Bytes()
}

func streamServer(t *testing.T, status int, body []byte) (*httptest.Server, *http.Request, *[]byte) {
	t.Helper()

	var gotReq http.Request
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = *r
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		w.Write(body) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	return srv, &gotReq, &gotBody
}

// stallServer writes prefix, if any, and then goes quiet until the test ends, so
// only the fetch deadline can end the exchange. The handler is released before
// the server is closed; Close waits for handlers to return.
func stallServer(t *testing.T, prefix []byte) *httptest.Server {
	t.Helper()

	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		if prefix != nil {
			w.WriteHeader(http.StatusOK)
			w.Write(prefix) //nolint:errcheck
			w.(http.Flusher).Flush()
		}
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	return srv
}

func TestFetchLogsFromSource(t *testing.T) {
	sourceArchive := []tarEntry{
		{name: "binlog.000004", content: "tail-of-four"},
		{name: "binlog.000005", content: magic + "whole-five"},
		{name: "binlog.000006", content: magic + "whole-six"},
	}

	t.Run("stages every log in rotation order", func(t *testing.T) {
		srv, req, body := streamServer(t, http.StatusOK, tarBytes(t, sourceArchive...))
		staging := filepath.Join(t.TempDir(), "source-logs")

		logs, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157, testFetchTimeout)

		require.NoError(t, err)
		require.Len(t, logs, 3)
		for i, e := range sourceArchive {
			assert.Equal(t, filepath.Join(staging, e.name), logs[i])
			got, err := os.ReadFile(logs[i])
			require.NoError(t, err)
			assert.Equal(t, e.content, string(got))
		}

		assert.Equal(t, http.MethodPost, req.Method)
		assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
		assert.JSONEq(t, `{"binary_log":"binlog.000004","position":157}`, string(*body))
	})

	t.Run("wipes stale leftovers", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t, sourceArchive[0]))
		staging := filepath.Join(t.TempDir(), "source-logs")
		markStaged(t, staging)
		require.NoError(t, os.MkdirAll(filepath.Join(staging, "nested"), 0o755))
		writeFile(t, staging, "binlog.999999", []byte("STALE"))

		logs, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157, testFetchTimeout)

		require.NoError(t, err)
		require.Len(t, logs, 1)
		assert.Equal(t, []string{"binlog.000004"}, stagedNames(t, staging))
	})

	t.Run("stages into an empty directory", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t, sourceArchive[0]))
		staging := filepath.Join(t.TempDir(), "source-logs")
		require.NoError(t, os.MkdirAll(staging, 0o755))

		logs, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157, testFetchTimeout)

		require.NoError(t, err)
		require.Len(t, logs, 1)
		assert.Equal(t, []string{"binlog.000004"}, stagedNames(t, staging))
	})

	t.Run("refuses to wipe a directory it did not create", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t, sourceArchive[0]))
		staging := filepath.Join(t.TempDir(), "mysql")
		require.NoError(t, os.MkdirAll(staging, 0o755))
		writeFile(t, staging, "ibdata1", []byte("DATA"))

		_, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157, testFetchTimeout)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "it is not a staging directory this job may wipe")
		assert.FileExists(t, filepath.Join(staging, "ibdata1"), "nothing may be deleted")
	})

	t.Run("keeps leftovers when the source refuses", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusNotFound, []byte("requested binary log is not available\n"))
		staging := filepath.Join(t.TempDir(), "source-logs")
		require.NoError(t, os.MkdirAll(staging, 0o755))
		writeFile(t, staging, "binlog.999999", []byte("STALE"))

		_, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157, testFetchTimeout)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status: 404")
		leftover, err := os.ReadFile(filepath.Join(staging, "binlog.999999"))
		require.NoError(t, err)
		assert.Equal(t, "STALE", string(leftover))
	})

	t.Run("empty archive", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t))

		_, err := fetchLogsFromSource(t.Context(), filepath.Join(t.TempDir(), "source-logs"),
			srv.URL, "binlog.000004", 157, testFetchTimeout)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "source streamed no binary logs")
	})

	t.Run("skips directory entries", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t,
			tarEntry{name: "subdir/", typeflag: tar.TypeDir},
			sourceArchive[0]))
		staging := filepath.Join(t.TempDir(), "source-logs")

		logs, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157, testFetchTimeout)

		require.NoError(t, err)
		assert.Equal(t, []string{filepath.Join(staging, "binlog.000004")}, logs)
	})

	t.Run("skips symlink entries", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t,
			tarEntry{name: "binlog.000003", content: "/etc/passwd", typeflag: tar.TypeSymlink},
			sourceArchive[0]))
		staging := filepath.Join(t.TempDir(), "source-logs")

		logs, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157, testFetchTimeout)

		require.NoError(t, err)
		assert.Equal(t, []string{filepath.Join(staging, "binlog.000004")}, logs)
		_, err = os.Lstat(filepath.Join(staging, "binlog.000003"))
		assert.True(t, os.IsNotExist(err), "no symlink may be staged")
	})

	t.Run("strips directories from entry names", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t,
			tarEntry{name: "../../etc/binlog.000004", content: "tail-of-four"}))
		staging := filepath.Join(t.TempDir(), "source-logs")

		logs, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157, testFetchTimeout)

		require.NoError(t, err)
		assert.Equal(t, []string{filepath.Join(staging, "binlog.000004")}, logs)
	})

	t.Run("duplicate entry names", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t,
			tarEntry{name: "binlog.000004", content: "first"},
			tarEntry{name: "binlog.000004", content: "second"}))
		staging := filepath.Join(t.TempDir(), "source-logs")

		logs, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157, testFetchTimeout)

		require.NoError(t, err)
		assert.Equal(t, []string{
			filepath.Join(staging, "binlog.000004"),
			filepath.Join(staging, "binlog.000004"),
		}, logs)
		got, err := os.ReadFile(logs[0])
		require.NoError(t, err)
		assert.Equal(t, "second", string(got))
	})

	t.Run("zero file mode from the wire", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores file modes")
		}

		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: "binlog.000004", Mode: 0, Size: 4, Typeflag: tar.TypeReg,
		}))
		_, err := tw.Write([]byte("BBBB"))
		require.NoError(t, err)
		require.NoError(t, tw.Close())

		srv, _, _ := streamServer(t, http.StatusOK, buf.Bytes())
		staging := filepath.Join(t.TempDir(), "source-logs")

		logs, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157, testFetchTimeout)

		require.NoError(t, err)
		require.Len(t, logs, 1)
		_, err = os.ReadFile(logs[0])
		require.Error(t, err, "the staged log is unreadable, so the splice will fail later")
		assert.Contains(t, err.Error(), "permission denied")
	})

	t.Run("truncated body", func(t *testing.T) {
		full := tarBytes(t, tarEntry{name: "binlog.000004", content: strings.Repeat("x", 2048)})
		srv, _, _ := streamServer(t, http.StatusOK, full[:600])

		_, err := fetchLogsFromSource(t.Context(), filepath.Join(t.TempDir(), "source-logs"),
			srv.URL, "binlog.000004", 157, testFetchTimeout)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected EOF")
	})

	t.Run("waits for a sidecar that is not listening yet", func(t *testing.T) {
		addr := freeAddr(t)
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(tarBytes(t, sourceArchive[0]))
		}))
		t.Cleanup(srv.Close)
		go func() {
			time.Sleep(3 * sidecarPoll)
			l, err := net.Listen("tcp", addr)
			if err != nil {
				t.Errorf("listen on %s: %v", addr, err)
				return
			}
			srv.Listener = l
			srv.Start()
		}()
		staging := filepath.Join(t.TempDir(), "source-logs")

		logs, err := fetchLogsFromSource(t.Context(), staging, "http://"+addr, "binlog.000004", 157, testFetchTimeout)

		require.NoError(t, err)
		assert.Equal(t, []string{filepath.Join(staging, "binlog.000004")}, logs)
	})

	t.Run("gives up on a sidecar that never listens at the deadline", func(t *testing.T) {
		_, err := fetchLogsFromSource(t.Context(), filepath.Join(t.TempDir(), "source-logs"),
			"http://"+freeAddr(t), "binlog.000004", 157, 3*sidecarPoll)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "source did not finish streaming within")
		assert.Contains(t, err.Error(), "connection refused")
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("a source that stalls on the headers gives up at the deadline", func(t *testing.T) {
		srv := stallServer(t, nil)

		_, err := fetchLogsFromSource(t.Context(), filepath.Join(t.TempDir(), "source-logs"),
			srv.URL, "binlog.000004", 157, 50*time.Millisecond)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "source did not finish streaming within 50ms")
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("a source that stalls mid-body gives up at the deadline", func(t *testing.T) {
		srv := stallServer(t, tarBytes(t, sourceArchive...)[:64])

		_, err := fetchLogsFromSource(t.Context(), filepath.Join(t.TempDir(), "source-logs"),
			srv.URL, "binlog.000004", 157, 50*time.Millisecond)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "source did not finish streaming within 50ms")
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("cancelled context", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t, sourceArchive...))
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := fetchLogsFromSource(ctx, filepath.Join(t.TempDir(), "source-logs"),
			srv.URL, "binlog.000004", 157, testFetchTimeout)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "stream logs from source")
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("source unreachable", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, nil)
		url := srv.URL
		srv.Close()

		_, err := fetchLogsFromSource(t.Context(), filepath.Join(t.TempDir(), "source-logs"),
			url, "binlog.000004", 157, testFetchTimeout)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "stream logs from source")
	})

	t.Run("staging dir cannot be read", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t, sourceArchive[0]))
		dir := t.TempDir()
		writeFile(t, dir, "blocker", []byte("not a directory"))

		_, err := fetchLogsFromSource(t.Context(), filepath.Join(dir, "blocker", "source-logs"),
			srv.URL, "binlog.000004", 157, testFetchTimeout)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read dir ")
	})

	t.Run("staging dir cannot be created", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}

		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t, sourceArchive[0]))
		dir := t.TempDir()
		require.NoError(t, os.Chmod(dir, 0o555))
		t.Cleanup(func() { os.Chmod(dir, 0o755) }) //nolint:errcheck

		_, err := fetchLogsFromSource(t.Context(), filepath.Join(dir, "source-logs"),
			srv.URL, "binlog.000004", 157, testFetchTimeout)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "create dir ")
	})
}

const (
	drainedState       = "Replica has read all relay log; waiting for more updates"
	legacyDrainedState = "Slave has read all relay log; waiting for more updates"
	busyState          = "Waiting for an event from Coordinator"
)

type fakeStatuser struct {
	statuses []map[string]string
	cycle    bool // replay the script instead of holding the last entry
	err      error
	calls    int
	onCall   func(calls int) // runs before the status is returned
}

var _ replicaStatuser = (*fakeStatuser)(nil)

func (f *fakeStatuser) ShowReplicaStatus(context.Context) (map[string]string, error) {
	f.calls++

	if f.onCall != nil {
		f.onCall(f.calls)
	}

	if f.err != nil {
		return nil, f.err
	}
	if len(f.statuses) == 0 {
		return nil, errors.New("fake: no statuses scripted")
	}

	if f.cycle {
		return f.statuses[(f.calls-1)%len(f.statuses)], nil
	}

	return f.statuses[min(f.calls-1, len(f.statuses)-1)], nil
}

type vanishingStatuser struct {
	inner *fakeStatuser
	after int
}

var _ replicaStatuser = (*vanishingStatuser)(nil)

func (v *vanishingStatuser) ShowReplicaStatus(ctx context.Context) (map[string]string, error) {
	if v.inner.calls >= v.after {
		return nil, sql.ErrNoRows
	}

	return v.inner.ShowReplicaStatus(ctx)
}

func applying(file string, pos uint64, state string) map[string]string {
	return map[string]string{
		"Replica_SQL_Running":       "Yes",
		"Replica_SQL_Running_State": state,
		"Relay_Log_File":            file,
		"Relay_Log_Pos":             strconv.FormatUint(pos, 10),
	}
}

func TestWaitForRelayLogsApplied(t *testing.T) {
	const (
		target     = "relay-bin.000002"
		startPos   = uint64(100)
		poll       = time.Millisecond
		patience   = time.Second
		impatience = 50 * time.Millisecond
	)

	logs := relayIndex{"relay-bin.000001", target, "relay-bin.000003"}

	t.Run("succeeds once the applier is drained, stable and past startPos", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{
			applying(target, 150, busyState),
			applying(target, 220, drainedState),
			applying(target, 220, drainedState),
		}}

		require.NoError(t, waitApplied(t, f, logs, nil, target, startPos, poll, patience))
		// Stability needs the same position twice, so poll 1 can never finish it.
		assert.GreaterOrEqual(t, f.calls, 3)
	})

	t.Run("two polls are the minimum", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying(target, 220, drainedState)}}

		require.NoError(t, waitApplied(t, f, logs, nil, target, startPos, poll, patience))
		assert.Equal(t, 2, f.calls)
	})

	t.Run("startPos 0 only requires the applier to drain", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying(target, 4, drainedState)}}

		require.NoError(t, waitApplied(t, f, logs, nil, target, 0, poll, patience))
	})

	t.Run("a later relay log counts as progress", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying("relay-bin.000003", 4, drainedState)}}

		require.NoError(t, waitApplied(t, f, logs, nil, target, startPos, poll, patience))
	})

	t.Run("a lower suffix later in the index counts as progress", func(t *testing.T) {
		mixed := relayIndex{"relay-bin.000009", target, "new-relay.000001"}
		f := &fakeStatuser{statuses: []map[string]string{applying("new-relay.000001", 4, drainedState)}}

		require.NoError(t, waitApplied(t, f, mixed, nil, target, startPos, poll, patience))
	})

	t.Run("a higher suffix earlier in the index is not progress", func(t *testing.T) {
		mixed := relayIndex{"relay-bin.000009", target, "new-relay.000001"}
		f := &fakeStatuser{statuses: []map[string]string{applying("relay-bin.000009", 999, drainedState)}}

		err := waitApplied(t, f, mixed, nil, target, startPos, poll, impatience)

		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})

	t.Run("the pre-8.0.22 state wording still counts as drained", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying(target, 220, legacyDrainedState)}}

		require.NoError(t, waitApplied(t, f, logs, nil, target, startPos, poll, patience))
	})

	t.Run("an empty poll does not reset progress", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{
			applying(target, 101, drainedState),
			{"Replica_SQL_Running": "Yes", "Relay_Log_File": ""},
			applying(target, 101, drainedState),
		}}

		require.NoError(t, waitApplied(t, f, logs, nil, target, startPos, poll, patience))
		assert.Equal(t, 3, f.calls)
	})

	t.Run("waits through an uninitialized applier", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{
			{"Replica_SQL_Running": "Yes", "Relay_Log_File": "", "Relay_Log_Pos": "garbage"},
			applying(target, 220, drainedState),
			applying(target, 220, drainedState),
		}}

		require.NoError(t, waitApplied(t, f, logs, nil, target, startPos, poll, patience))
		assert.Equal(t, 3, f.calls)
	})

	timeouts := []struct {
		name     string
		statuses []map[string]string
		cycle    bool
	}{
		{
			name:     "drained and stable in an earlier relay log",
			statuses: []map[string]string{applying("relay-bin.000001", 999, drainedState)},
		},
		{
			name:     "before startPos in the target log",
			statuses: []map[string]string{applying(target, 50, drainedState)},
		},
		{
			name:     "exactly at startPos",
			statuses: []map[string]string{applying(target, startPos, drainedState)},
		},
		{
			name:     "past startPos but never drained",
			statuses: []map[string]string{applying(target, 500, busyState)},
		},
		{
			name:     "no running state reported",
			statuses: []map[string]string{applying(target, 500, "")},
		},
		{
			name: "drained but the position keeps moving",
			statuses: []map[string]string{
				applying(target, 220, drainedState),
				applying(target, 260, drainedState),
			},
			cycle: true,
		},
		{
			name:     "the applier never initializes",
			statuses: []map[string]string{{"Replica_SQL_Running": "Yes", "Relay_Log_File": ""}},
		},
	}

	for _, tt := range timeouts {
		t.Run("times out: "+tt.name, func(t *testing.T) {
			f := &fakeStatuser{statuses: tt.statuses, cycle: tt.cycle}

			err := waitApplied(t, f, logs, nil, target, startPos, poll, impatience)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "gave up after")
			assert.ErrorIs(t, err, context.DeadlineExceeded)
		})
	}

	failures := []struct {
		name     string
		relayLog string
		statuses []map[string]string
		wantErr  string
	}{
		{
			name:     "SQL thread failed",
			relayLog: target,
			statuses: []map[string]string{{
				"Replica_SQL_Running": "Yes",
				"Last_SQL_Error":      "Error 1062: Duplicate entry",
			}},
			wantErr: "SQL_THREAD failed: Error 1062: Duplicate entry",
		},
		{
			name:     "a failed thread reports the error, not just that it stopped",
			relayLog: target,
			statuses: []map[string]string{{
				"Replica_SQL_Running": "No",
				"Last_SQL_Error":      "Error 1062: Duplicate entry",
			}},
			wantErr: "SQL_THREAD failed",
		},
		{
			name:     "SQL thread stopped",
			relayLog: target,
			statuses: []map[string]string{{"Replica_SQL_Running": "No"}},
			wantErr:  "SQL_THREAD is not running",
		},
		{
			name:     "SQL thread state missing",
			relayLog: target,
			statuses: []map[string]string{{}},
			wantErr:  "SQL_THREAD is not running",
		},
		{
			name:     "lowercase yes is not Yes",
			relayLog: target,
			statuses: []map[string]string{{"Replica_SQL_Running": "yes"}},
			wantErr:  "SQL_THREAD is not running",
		},
		{
			name:     "unparseable Relay_Log_Pos",
			relayLog: target,
			statuses: []map[string]string{{
				"Replica_SQL_Running": "Yes",
				"Relay_Log_File":      target,
				"Relay_Log_Pos":       "x",
			}},
			wantErr: "parse Relay_Log_Pos",
		},
		{
			name:     "a current relay log the index does not list",
			relayLog: target,
			statuses: []map[string]string{applying("garbage", 500, drainedState)},
			wantErr:  "the applier is on garbage, which the index does not list",
		},
	}

	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStatuser{statuses: tt.statuses}

			err := waitApplied(t, f, logs, nil, tt.relayLog, startPos, poll, impatience)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}

	t.Run("a target the index does not list fails before polling", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying(target, 220, drainedState)}}

		err := waitApplied(t, f, logs, nil, "relay-bin", startPos, poll, patience)

		require.Error(t, err)
		assert.Contains(t, err.Error(), `the index does not list the relay log "relay-bin"`)
		assert.Equal(t, 0, f.calls)
	})

	t.Run("the status query fails", func(t *testing.T) {
		f := &fakeStatuser{err: errors.New("connection lost")}

		err := waitApplied(t, f, logs, nil, target, startPos, poll, patience)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "show replica status: connection lost")
		assert.Equal(t, 1, f.calls)
	})

	t.Run("a channel that disappears mid-wait is not a failure", func(t *testing.T) {
		inner := &fakeStatuser{statuses: []map[string]string{applying(target, 150, busyState)}}
		v := &vanishingStatuser{inner: inner, after: 2}

		require.NoError(t, waitApplied(t, v, logs, nil, target, startPos, poll, patience))
		assert.Equal(t, 2, inner.calls, "the wait ends on the read that finds no rows")
	})

	t.Run("a channel that is already gone is not a failure", func(t *testing.T) {
		f := &fakeStatuser{err: sql.ErrNoRows}

		require.NoError(t, waitApplied(t, f, logs, nil, target, startPos, poll, patience))
	})

	t.Run("the give-up message reports where the applier stopped", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying(target, startPos, drainedState)}}

		err := waitApplied(t, f, logs, nil, target, startPos, poll, 20*time.Millisecond)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "gave up after ")
		assert.Contains(t, err.Error(), "at relay-bin.000002:100")
	})

	t.Run("a long poll interval does not delay the timeout", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying(target, startPos, drainedState)}}

		start := time.Now()
		err := waitApplied(t, f, logs, nil, target, startPos, time.Second, 10*time.Millisecond)

		require.Error(t, err)
		assert.Less(t, time.Since(start), 500*time.Millisecond)
	})

	t.Run("an already cancelled context", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying(target, startPos, drainedState)}}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := waitForRelayLogsApplied(ctx, f, logs, nil, target, startPos, poll)

		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 1, f.calls)
	})
}

func TestSourceHost(t *testing.T) {
	t.Run("reads the input", func(t *testing.T) {
		host, err := sourceHost("mysql-1.mysql")

		require.NoError(t, err)
		assert.Equal(t, "mysql-1.mysql", host)
	})

	t.Run("trims surrounding whitespace", func(t *testing.T) {
		host, err := sourceHost("  mysql-1.mysql\n")

		require.NoError(t, err)
		assert.Equal(t, "mysql-1.mysql", host)
	})

	t.Run("unset", func(t *testing.T) {
		_, err := sourceHost("")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "-source is not set")
	})

	t.Run("whitespace only", func(t *testing.T) {
		_, err := sourceHost("   ")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "-source is not set")
	})
}

func TestStagingPath(t *testing.T) {
	t.Run("reads the input", func(t *testing.T) {
		dir, err := stagingPath("  /var/lib/mysql/source-logs\n")

		require.NoError(t, err)
		assert.Equal(t, "/var/lib/mysql/source-logs", dir)
	})

	t.Run("cleans the path", func(t *testing.T) {
		dir, err := stagingPath("/var/lib/mysql/../mysql/source-logs/")

		require.NoError(t, err)
		assert.Equal(t, "/var/lib/mysql/source-logs", dir)
	})

	t.Run("empty", func(t *testing.T) {
		_, err := stagingPath("   ")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "-staging-dir is not set")
	})

	t.Run("relative", func(t *testing.T) {
		_, err := stagingPath("source-logs")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not an absolute path")
	})

	t.Run("filesystem root", func(t *testing.T) {
		for _, dir := range []string{"/", "//", "/.."} {
			_, err := stagingPath(dir)

			require.Error(t, err, dir)
			assert.Contains(t, err.Error(), "must not be the filesystem root")
		}
	})

	t.Run("holds the data directory", func(t *testing.T) {
		for _, dir := range []string{"/var/lib/mysql", "/var/lib/mysql/", "/var/lib", "/var"} {
			_, err := stagingPath(dir)

			require.Error(t, err, dir)
			assert.Contains(t, err.Error(), "holds the MySQL data directory")
		}
	})
}

func TestSourceStreamURL(t *testing.T) {
	port := strconv.Itoa(mysql.SidecarHTTPPort)

	tests := []struct {
		name     string
		host     string
		expected string
	}{
		{
			name:     "hostname",
			host:     "mysql-1.mysql",
			expected: "http://mysql-1.mysql:" + port + "/failover/stream",
		},
		{
			name:     "IPv4 address",
			host:     "10.0.1.7",
			expected: "http://10.0.1.7:" + port + "/failover/stream",
		},
		{
			// JoinHostPort brackets a host that contains colons.
			name:     "IPv6 address",
			host:     "fd00::1",
			expected: "http://[fd00::1]:" + port + "/failover/stream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sourceStreamURL(tt.host)

			assert.Equal(t, tt.expected, got)

			// The URL has to be usable as-is, and carry the sidecar's port.
			u, err := url.Parse(got)
			require.NoError(t, err)
			assert.Equal(t, "http", u.Scheme)
			assert.Equal(t, "/failover/stream", u.Path)
			assert.Equal(t, port, u.Port())
		})
	}
}

// waitApplied bounds the drain the way the job does, through the context: the
// wait has no budget of its own any more.
func waitApplied(
	t *testing.T,
	s replicaStatuser,
	relayLogs relayIndex,
	recovered <-chan struct{},
	relayLog string,
	startPos uint64,
	poll, timeout time.Duration,
) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()

	return waitForRelayLogsApplied(ctx, s, relayLogs, recovered, relayLog, startPos, poll)
}

// freeAddr returns a local address nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	return addr
}
