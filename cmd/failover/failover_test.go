package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
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

func withMagic(payload string) []byte {
	return append([]byte(magic), payload...)
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

func TestGetLogIndex(t *testing.T) {
	tests := []struct {
		name     string
		logName  string
		expected uint64
		wantErr  string
	}{
		{name: "bare name", logName: "relay-bin.000004", expected: 4},
		{name: "full path", logName: "/var/lib/mysql/relay-bin.000123", expected: 123},
		{name: "no leading zeros", logName: "relay-bin.1", expected: 1},
		{name: "six digits", logName: "relay-bin.999999", expected: 999999},
		{name: "dot in the directory name", logName: "/var/lib/my.data/relay-bin.000007", expected: 7},
		{name: "no suffix", logName: "relay-bin", wantErr: "invalid syntax"},
		{name: "empty", logName: "", wantErr: "invalid syntax"},
		{name: "the index file", logName: "relay-bin.index", wantErr: "invalid syntax"},
		{name: "extra suffix", logName: "relay-bin.000004.bak", wantErr: "invalid syntax"},
		{name: "trailing dot", logName: "relay-bin.000004.", wantErr: "invalid syntax"},
		{name: "signed", logName: "relay-bin.-1", wantErr: "invalid syntax"},
		{name: "overflows uint64", logName: "relay-bin.18446744073709551616", wantErr: "value out of range"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, err := getLogIndex(tt.logName)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, idx)
		})
	}
}

func TestLastClosedRelayLog(t *testing.T) {
	tests := []struct {
		name     string
		entries  []string
		expected string
		wantErr  string
	}{
		{
			name:     "three entries returns the second to last",
			entries:  []string{"relay-bin.000001", "relay-bin.000002", "relay-bin.000003"},
			expected: "relay-bin.000002",
		},
		{
			name:     "exactly two entries returns the first",
			entries:  []string{"relay-bin.000001", "relay-bin.000002"},
			expected: "relay-bin.000001",
		},
		{
			name:     "entries carry a directory prefix",
			entries:  []string{"./relay-bin.000001", "/other/dir/relay-bin.000002", "./relay-bin.000003"},
			expected: "relay-bin.000002",
		},
		{
			name:     "blank lines are skipped, not counted",
			entries:  []string{"relay-bin.000001", "", "  ", "relay-bin.000002", "relay-bin.000003", ""},
			expected: "relay-bin.000002",
		},
		{
			name:     "CRLF line endings",
			entries:  []string{"relay-bin.000001\r", "relay-bin.000002\r", "relay-bin.000003\r"},
			expected: "relay-bin.000002",
		},
		{
			name:     "leading whitespace is trimmed",
			entries:  []string{"\trelay-bin.000001", "  relay-bin.000002"},
			expected: "relay-bin.000001",
		},
		{
			name:    "a single entry has no closed relay log",
			entries: []string{"relay-bin.000001"},
			wantErr: "lists 1 relay log(s), so no relay log is closed",
		},
		{
			name:    "an empty index",
			entries: nil,
			wantErr: "lists 0 relay log(s)",
		},
		{
			name:    "only blank lines",
			entries: []string{"", "  ", ""},
			wantErr: "lists 0 relay log(s)",
		},
		{
			name:     "two basenames in one index",
			entries:  []string{"old-relay.000009", "new-relay.000001", "new-relay.000002"},
			expected: "new-relay.000001",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index := filepath.Join(t.TempDir(), "relay-bin.index")
			writeIndex(t, index, tt.entries...)
			relayDir := t.TempDir()

			got, err := lastClosedRelayLog(index, relayDir)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, filepath.Join(relayDir, tt.expected), got)
		})
	}

	t.Run("index is missing", func(t *testing.T) {
		_, err := lastClosedRelayLog(filepath.Join(t.TempDir(), "relay-bin.index"), t.TempDir())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "open ")
		assert.Contains(t, err.Error(), "no such file")
	})

	t.Run("index is a directory", func(t *testing.T) {
		_, err := lastClosedRelayLog(t.TempDir(), t.TempDir())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read ")
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

		target, startPos, err := updateRelayLogs(newSourceLogs(t), "relay-bin.000002", l.basename, l.index)

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

		target, startPos, err := updateRelayLogs(newSourceLogs(t), "relay-bin.000001", l.basename, l.index)

		require.NoError(t, err)
		assert.Equal(t, l.target, target)
		assert.Equal(t, uint64(13), startPos)
		l.assertUntouched(t)
	})

	t.Run("refuses an applier ahead of the newest closed relay log", func(t *testing.T) {
		l := newRelayLayout(t)
		before, err := os.ReadFile(l.target)
		require.NoError(t, err)

		_, _, err = updateRelayLogs(newSourceLogs(t), "relay-bin.000003", l.basename, l.index)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "applier is on relay-bin.000003, ahead of the newest closed relay log relay-bin.000002")

		after, err := os.ReadFile(l.target)
		require.NoError(t, err)
		assert.Equal(t, before, after, "the target must never be opened for write")
		l.assertUntouched(t)
	})

	t.Run("only one relay log in the index", func(t *testing.T) {
		l := newRelayLayout(t)
		writeIndex(t, l.index, "./relay-bin.000001")

		_, _, err := updateRelayLogs(newSourceLogs(t), "relay-bin.000001", l.basename, l.index)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "get relay log to append to")
		assert.Contains(t, err.Error(), "so no relay log is closed")
	})

	t.Run("target listed in the index but missing on disk", func(t *testing.T) {
		l := newRelayLayout(t)
		require.NoError(t, os.Remove(l.target))

		_, _, err := updateRelayLogs(newSourceLogs(t), "relay-bin.000002", l.basename, l.index)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "stat ")
		assert.Contains(t, err.Error(), "no such file")
	})

	t.Run("target is a directory", func(t *testing.T) {
		l := newRelayLayout(t)
		require.NoError(t, os.Remove(l.target))
		require.NoError(t, os.Mkdir(l.target, 0o755))

		_, _, err := updateRelayLogs(newSourceLogs(t), "relay-bin.000002", l.basename, l.index)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "open ")
	})

	t.Run("unparseable applier relay log name", func(t *testing.T) {
		l := newRelayLayout(t)

		_, _, err := updateRelayLogs(newSourceLogs(t), "relay-bin", l.basename, l.index)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "get relay log (relay-bin) index")
		l.assertUntouched(t)
	})

	t.Run("uninitialized applier", func(t *testing.T) {
		l := newRelayLayout(t)

		_, _, err := updateRelayLogs(newSourceLogs(t), "", l.basename, l.index)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "get relay log () index")
	})

	t.Run("a later source log with a bad magic leaves a partial splice", func(t *testing.T) {
		l := newRelayLayout(t)
		before, err := os.ReadFile(l.target)
		require.NoError(t, err)

		srcDir := t.TempDir()
		sourceLogs := []string{
			writeFile(t, srcDir, "binlog.000004", []byte("tail-of-four")),
			writeFile(t, srcDir, "binlog.000005", []byte("NOPEwhole-five")),
		}

		_, _, err = updateRelayLogs(sourceLogs, "relay-bin.000002", l.basename, l.index)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected magic number")

		after, err := os.ReadFile(l.target)
		require.NoError(t, err)
		assert.Equal(t, string(before)+"tail-of-four", string(after))
	})

	t.Run("a missing source log", func(t *testing.T) {
		l := newRelayLayout(t)
		before, err := os.ReadFile(l.target)
		require.NoError(t, err)

		_, _, err = updateRelayLogs([]string{filepath.Join(t.TempDir(), "binlog.000004")},
			"relay-bin.000002", l.basename, l.index)

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

		target, startPos, err := updateRelayLogs([]string{empty}, "relay-bin.000002", l.basename, l.index)

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

		target, startPos, err := updateRelayLogs([]string{empty}, "relay-bin.000003", l.basename, l.index)

		require.NoError(t, err)
		assert.Equal(t, "relay-bin.000003", target)
		assert.Equal(t, uint64(0), startPos)
	})

	t.Run("no source logs", func(t *testing.T) {
		l := newRelayLayout(t)
		writeIndex(t, l.index, "./relay-bin.000003")

		target, startPos, err := updateRelayLogs(nil, "relay-bin.000003", l.basename, l.index)

		require.NoError(t, err)
		assert.Equal(t, "relay-bin.000003", target)
		assert.Equal(t, uint64(0), startPos)
	})

	t.Run("nothing to splice tolerates an uninitialized applier", func(t *testing.T) {
		l := newRelayLayout(t)

		target, startPos, err := updateRelayLogs(nil, "", l.basename, l.index)

		require.NoError(t, err)
		assert.Empty(t, target)
		assert.Equal(t, uint64(0), startPos)
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

func TestFetchLogsFromSource(t *testing.T) {
	sourceArchive := []tarEntry{
		{name: "binlog.000004", content: "tail-of-four"},
		{name: "binlog.000005", content: magic + "whole-five"},
		{name: "binlog.000006", content: magic + "whole-six"},
	}

	t.Run("stages every log in rotation order", func(t *testing.T) {
		srv, req, body := streamServer(t, http.StatusOK, tarBytes(t, sourceArchive...))
		staging := filepath.Join(t.TempDir(), "source-logs")

		logs, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157)

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
		require.NoError(t, os.MkdirAll(filepath.Join(staging, "nested"), 0o755))
		writeFile(t, staging, "binlog.999999", []byte("STALE"))

		logs, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157)

		require.NoError(t, err)
		require.Len(t, logs, 1)
		entries, err := os.ReadDir(staging)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "binlog.000004", entries[0].Name())
	})

	t.Run("keeps leftovers when the source refuses", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusNotFound, []byte("requested binary log is not available\n"))
		staging := filepath.Join(t.TempDir(), "source-logs")
		require.NoError(t, os.MkdirAll(staging, 0o755))
		writeFile(t, staging, "binlog.999999", []byte("STALE"))

		_, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status: 404")
		leftover, err := os.ReadFile(filepath.Join(staging, "binlog.999999"))
		require.NoError(t, err)
		assert.Equal(t, "STALE", string(leftover))
	})

	t.Run("empty archive", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t))

		_, err := fetchLogsFromSource(t.Context(), filepath.Join(t.TempDir(), "source-logs"),
			srv.URL, "binlog.000004", 157)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "source streamed no binary logs")
	})

	t.Run("skips directory entries", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t,
			tarEntry{name: "subdir/", typeflag: tar.TypeDir},
			sourceArchive[0]))
		staging := filepath.Join(t.TempDir(), "source-logs")

		logs, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157)

		require.NoError(t, err)
		assert.Equal(t, []string{filepath.Join(staging, "binlog.000004")}, logs)
	})

	t.Run("skips symlink entries", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t,
			tarEntry{name: "binlog.000003", content: "/etc/passwd", typeflag: tar.TypeSymlink},
			sourceArchive[0]))
		staging := filepath.Join(t.TempDir(), "source-logs")

		logs, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157)

		require.NoError(t, err)
		assert.Equal(t, []string{filepath.Join(staging, "binlog.000004")}, logs)
		_, err = os.Lstat(filepath.Join(staging, "binlog.000003"))
		assert.True(t, os.IsNotExist(err), "no symlink may be staged")
	})

	t.Run("strips directories from entry names", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t,
			tarEntry{name: "../../etc/binlog.000004", content: "tail-of-four"}))
		staging := filepath.Join(t.TempDir(), "source-logs")

		logs, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157)

		require.NoError(t, err)
		assert.Equal(t, []string{filepath.Join(staging, "binlog.000004")}, logs)
	})

	t.Run("duplicate entry names", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t,
			tarEntry{name: "binlog.000004", content: "first"},
			tarEntry{name: "binlog.000004", content: "second"}))
		staging := filepath.Join(t.TempDir(), "source-logs")

		logs, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157)

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

		logs, err := fetchLogsFromSource(t.Context(), staging, srv.URL, "binlog.000004", 157)

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
			srv.URL, "binlog.000004", 157)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected EOF")
	})

	t.Run("cancelled context", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t, sourceArchive...))
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := fetchLogsFromSource(ctx, filepath.Join(t.TempDir(), "source-logs"),
			srv.URL, "binlog.000004", 157)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "stream logs from source")
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("source unreachable", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, nil)
		url := srv.URL
		srv.Close()

		_, err := fetchLogsFromSource(t.Context(), filepath.Join(t.TempDir(), "source-logs"),
			url, "binlog.000004", 157)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "stream logs from source")
	})

	t.Run("staging dir cannot be wiped", func(t *testing.T) {
		srv, _, _ := streamServer(t, http.StatusOK, tarBytes(t, sourceArchive[0]))
		dir := t.TempDir()
		writeFile(t, dir, "blocker", []byte("not a directory"))

		_, err := fetchLogsFromSource(t.Context(), filepath.Join(dir, "blocker", "source-logs"),
			srv.URL, "binlog.000004", 157)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "remove dir ")
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
			srv.URL, "binlog.000004", 157)

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
}

var _ replicaStatuser = (*fakeStatuser)(nil)

func (f *fakeStatuser) ShowReplicaStatus(context.Context) (map[string]string, error) {
	f.calls++

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

	t.Run("succeeds once the applier is drained, stable and past startPos", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{
			applying(target, 150, busyState),
			applying(target, 220, drainedState),
			applying(target, 220, drainedState),
		}}

		require.NoError(t, waitForRelayLogsApplied(t.Context(), f, target, startPos, poll, patience))
		// Stability needs the same position twice, so poll 1 can never finish it.
		assert.GreaterOrEqual(t, f.calls, 3)
	})

	t.Run("two polls are the minimum", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying(target, 220, drainedState)}}

		require.NoError(t, waitForRelayLogsApplied(t.Context(), f, target, startPos, poll, patience))
		assert.Equal(t, 2, f.calls)
	})

	t.Run("startPos 0 only requires the applier to drain", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying(target, 4, drainedState)}}

		require.NoError(t, waitForRelayLogsApplied(t.Context(), f, target, 0, poll, patience))
	})

	t.Run("a later relay log counts as progress", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying("relay-bin.000003", 4, drainedState)}}

		require.NoError(t, waitForRelayLogsApplied(t.Context(), f, target, startPos, poll, patience))
	})

	t.Run("the pre-8.0.22 state wording still counts as drained", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying(target, 220, legacyDrainedState)}}

		require.NoError(t, waitForRelayLogsApplied(t.Context(), f, target, startPos, poll, patience))
	})

	t.Run("an empty poll does not reset progress", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{
			applying(target, 101, drainedState),
			{"Replica_SQL_Running": "Yes", "Relay_Log_File": ""},
			applying(target, 101, drainedState),
		}}

		require.NoError(t, waitForRelayLogsApplied(t.Context(), f, target, startPos, poll, patience))
		assert.Equal(t, 3, f.calls)
	})

	t.Run("waits through an uninitialized applier", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{
			{"Replica_SQL_Running": "Yes", "Relay_Log_File": "", "Relay_Log_Pos": "garbage"},
			applying(target, 220, drainedState),
			applying(target, 220, drainedState),
		}}

		require.NoError(t, waitForRelayLogsApplied(t.Context(), f, target, startPos, poll, patience))
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

			err := waitForRelayLogsApplied(t.Context(), f, target, startPos, poll, impatience)

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
			name:     "unparseable current relay log name",
			relayLog: target,
			statuses: []map[string]string{applying("garbage", 500, drainedState)},
			wantErr:  "get relay log (garbage) index",
		},
	}

	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStatuser{statuses: tt.statuses}

			err := waitForRelayLogsApplied(t.Context(), f, tt.relayLog, startPos, poll, impatience)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}

	t.Run("an unparseable target fails before polling", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying(target, 220, drainedState)}}

		err := waitForRelayLogsApplied(t.Context(), f, "relay-bin", startPos, poll, patience)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "get relay log (relay-bin) index")
		assert.Equal(t, 0, f.calls)
	})

	t.Run("the status query fails", func(t *testing.T) {
		f := &fakeStatuser{err: errors.New("connection lost")}

		err := waitForRelayLogsApplied(t.Context(), f, target, startPos, poll, patience)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "show replica status: connection lost")
		assert.Equal(t, 1, f.calls)
	})

	t.Run("the give-up message reports the injected timeout", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying(target, startPos, drainedState)}}

		err := waitForRelayLogsApplied(t.Context(), f, target, startPos, poll, 20*time.Millisecond)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "gave up after 20ms at relay-bin.000002:100")
	})

	t.Run("a long poll interval does not delay the timeout", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying(target, startPos, drainedState)}}

		start := time.Now()
		err := waitForRelayLogsApplied(t.Context(), f, target, startPos, time.Second, 10*time.Millisecond)

		require.Error(t, err)
		assert.Less(t, time.Since(start), 500*time.Millisecond)
	})

	t.Run("an already cancelled context", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{applying(target, startPos, drainedState)}}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := waitForRelayLogsApplied(ctx, f, target, startPos, poll, patience)

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

	t.Run("empty", func(t *testing.T) {
		_, err := stagingPath("   ")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "-staging-dir is not set")
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
