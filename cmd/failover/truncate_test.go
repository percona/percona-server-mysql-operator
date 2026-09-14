package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eventOfLength builds an event whose header declares a length the body does not
// match, which is what a partial write leaves behind.
func eventOfLength(payload string, declared uint32) string {
	e := []byte(binlogEvent(payload))
	binary.LittleEndian.PutUint32(e[9:13], declared)

	return string(e)
}

// tornEvent is an event whose last bytes never made it to disk.
func tornEvent(payload string) string {
	e := binlogEvent(payload)

	return e[:len(e)-3]
}

// stagedLogs writes what fetchLogsFromSource leaves in the staging dir: the first
// log cut at the position the replica had already read, every later one whole.
func stagedLogs(t *testing.T, contents ...string) []string {
	t.Helper()

	dir := t.TempDir()
	logs := make([]string, 0, len(contents))

	for i, content := range contents {
		if i > 0 {
			content = magic + content
		}
		logs = append(logs, writeFile(t, dir, fmt.Sprintf("binlog.%06d", 4+i), []byte(content)))
	}

	return logs
}

func readAll(t *testing.T, paths []string) []string {
	t.Helper()

	contents := make([]string, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		contents = append(contents, string(b))
	}

	return contents
}

func TestLastEventEnd(t *testing.T) {
	one := binlogEvent("one")
	two := binlogEvent("two")

	tests := map[string]struct {
		content string
		start   int64
		want    int64
	}{
		"a whole log walks to the end": {
			content: magic + one + two,
			start:   binlogStartPos,
			want:    int64(len(magic + one + two)),
		},
		"a log cut at a read position carries no magic number": {
			content: one + two,
			start:   0,
			want:    int64(len(one + two)),
		},
		"a body cut short stops at the last whole event": {
			content: magic + one + two + tornEvent("three"),
			start:   binlogStartPos,
			want:    int64(len(magic + one + two)),
		},
		"a header cut short stops at the last whole event": {
			content: magic + one + two + binlogEvent("three")[:10],
			start:   binlogStartPos,
			want:    int64(len(magic + one + two)),
		},
		"a length of zero stops the walk": {
			content: magic + one + eventOfLength("three", 0),
			start:   binlogStartPos,
			want:    int64(len(magic + one)),
		},
		"a length shorter than a header stops the walk": {
			content: magic + one + eventOfLength("three", eventHeaderLen-1),
			start:   binlogStartPos,
			want:    int64(len(magic + one)),
		},
		"a length past the end of the file stops the walk": {
			content: magic + one + eventOfLength("three", 1<<20),
			start:   binlogStartPos,
			want:    int64(len(magic + one)),
		},
		"a log the replica had read to the end is empty": {
			content: "",
			start:   0,
			want:    0,
		},
		"nothing but a magic number": {
			content: magic,
			start:   binlogStartPos,
			want:    binlogStartPos,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeFile(t, t.TempDir(), "binlog.000004", []byte(tt.content))

			got, err := lastEventEnd(path, tt.start)

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("a log shorter than its magic number", func(t *testing.T) {
		path := writeFile(t, t.TempDir(), "binlog.000005", []byte("\xfeb"))

		_, err := lastEventEnd(path, binlogStartPos)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "shorter than a binary log header")
	})

	t.Run("a missing log", func(t *testing.T) {
		_, err := lastEventEnd(filepath.Join(t.TempDir(), "binlog.000004"), 0)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such file")
	})
}

func TestTruncateTornTail(t *testing.T) {
	t.Run("leaves whole logs alone", func(t *testing.T) {
		logs := stagedLogs(t, binlogEvent("four-tail"), binlogEvent("whole-five"))
		before := readAll(t, logs)

		require.NoError(t, truncateTornTail(logs))

		assert.Equal(t, before, readAll(t, logs))
	})

	t.Run("cuts a partial event from the newest log", func(t *testing.T) {
		logs := stagedLogs(t,
			binlogEvent("four-tail"),
			binlogEvent("whole-five")+tornEvent("half-five"),
		)

		require.NoError(t, truncateTornTail(logs))

		assert.Equal(t, []string{
			binlogEvent("four-tail"),
			magic + binlogEvent("whole-five"),
		}, readAll(t, logs))
	})

	t.Run("cuts a partial event from a lone log", func(t *testing.T) {
		logs := stagedLogs(t, binlogEvent("four-tail")+tornEvent("half-four"))

		require.NoError(t, truncateTornTail(logs))

		assert.Equal(t, []string{binlogEvent("four-tail")}, readAll(t, logs))
	})

	t.Run("a newest log that is nothing but a partial event", func(t *testing.T) {
		logs := stagedLogs(t, tornEvent("half-four"))

		require.NoError(t, truncateTornTail(logs))

		assert.Equal(t, []string{""}, readAll(t, logs),
			"an empty staged log is what a caught-up replica already gets")
	})

	t.Run("refuses a partial event in an earlier log", func(t *testing.T) {
		logs := stagedLogs(t,
			binlogEvent("four-tail"),
			binlogEvent("whole-five")+tornEvent("half-five"),
			binlogEvent("whole-six"),
		)
		before := readAll(t, logs)

		err := truncateTornTail(logs)

		require.ErrorIs(t, err, errTornBinlog)
		assert.Contains(t, err.Error(), "binlog.000005")
		assert.Equal(t, before, readAll(t, logs), "no log may be cut when the splice is refused")
	})

	t.Run("refuses a partial event in the first of several logs", func(t *testing.T) {
		logs := stagedLogs(t, tornEvent("half-four"), binlogEvent("whole-five"))
		before := readAll(t, logs)

		err := truncateTornTail(logs)

		require.ErrorIs(t, err, errTornBinlog)
		assert.Equal(t, before, readAll(t, logs))
	})

	t.Run("a first log the replica had read to the end is not torn", func(t *testing.T) {
		logs := stagedLogs(t, "", binlogEvent("whole-five"))
		before := readAll(t, logs)

		require.NoError(t, truncateTornTail(logs))

		assert.Equal(t, before, readAll(t, logs))
	})

	t.Run("no logs", func(t *testing.T) {
		require.NoError(t, truncateTornTail(nil))
	})

	t.Run("a missing log", func(t *testing.T) {
		err := truncateTornTail([]string{filepath.Join(t.TempDir(), "binlog.000004")})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such file")
	})
}
