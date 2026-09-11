package handler

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const magic = "\xfebin"

func newHandler(t *testing.T) *FailoverHandler {
	t.Helper()

	return &FailoverHandler{DataDir: t.TempDir()}
}

func binlogFile(t *testing.T, h *FailoverHandler, name, payload string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(h.DataDir, name), []byte(magic+payload), 0o644))
}

func writeIndex(t *testing.T, h *FailoverHandler, name string, entries ...string) {
	t.Helper()

	content := ""
	if len(entries) > 0 {
		content = strings.Join(entries, "\n") + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(h.DataDir, name), []byte(content), 0o644))
}

func tarEntries(t *testing.T, archive []byte) ([]string, map[string]string) {
	t.Helper()

	names := make([]string, 0)
	contents := make(map[string]string)

	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)

		body, err := io.ReadAll(tr)
		require.NoError(t, err)

		names = append(names, hdr.Name)
		contents[hdr.Name] = string(body)
	}

	return names, contents
}

func TestBinlogIndexPath(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		expected  string
		wantErr   bool
	}{
		{name: "numbered binary log", requested: "binlog.000004", expected: "binlog.index"},
		{name: "another basename", requested: "mysql-bin.000001", expected: "mysql-bin.index"},
		{name: "single-digit suffix", requested: "binlog.1", expected: "binlog.index"},
		{name: "no suffix", requested: "binlog", wantErr: true},
		{name: "empty", requested: "", wantErr: true},
		{name: "the index itself trims to nothing", requested: ".index", wantErr: true},
		{name: "suffix only", requested: ".000004", wantErr: true},
		{name: "trailing slash has no extension", requested: "binlog.000004/", wantErr: true},
		{name: "relative traversal", requested: "../../../etc/passwd.000001", expected: "passwd.index"},
		{name: "absolute path", requested: "/etc/passwd.1", expected: "passwd.index"},
		{name: "embedded traversal", requested: "a/../../b.001", expected: "b.index"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandler(t)

			got, err := h.binlogIndexPath(tt.requested)

			if tt.wantErr {
				require.ErrorIs(t, err, errInvalidRequest)
				assert.Contains(t, err.Error(), "is not a binary log name")
				return
			}

			require.NoError(t, err)
			assert.Equal(t, filepath.Join(h.DataDir, tt.expected), got)
			assert.Equal(t, h.DataDir, filepath.Dir(got), "the index must resolve inside the data dir")
		})
	}
}

func TestGetBinlogsToStream(t *testing.T) {
	tests := []struct {
		name      string
		entries   []string
		requested string
		expected  []string
		wantErr   error
		wantErrIs string
	}{
		{
			name:      "from the middle returns it and everything after",
			entries:   []string{"binlog.000001", "binlog.000002", "binlog.000003", "binlog.000004"},
			requested: "binlog.000002",
			expected:  []string{"binlog.000002", "binlog.000003", "binlog.000004"},
		},
		{
			name:      "from the first returns everything",
			entries:   []string{"binlog.000001", "binlog.000002"},
			requested: "binlog.000001",
			expected:  []string{"binlog.000001", "binlog.000002"},
		},
		{
			name:      "the last returns only itself",
			entries:   []string{"binlog.000001", "binlog.000002"},
			requested: "binlog.000002",
			expected:  []string{"binlog.000002"},
		},
		{
			name:      "entries carry a directory prefix",
			entries:   []string{"/var/lib/mysql/binlog.000001", "./binlog.000002", "binlog.000003"},
			requested: "binlog.000002",
			expected:  []string{"./binlog.000002", "binlog.000003"},
		},
		{
			name:      "blank lines are skipped",
			entries:   []string{"binlog.000001", "", "  ", "binlog.000002"},
			requested: "binlog.000001",
			expected:  []string{"binlog.000001", "binlog.000002"},
		},
		{
			name:      "purged or unknown",
			entries:   []string{"binlog.000005", "binlog.000006"},
			requested: "binlog.000002",
			wantErr:   errBinlogNotFound,
		},
		{
			name:      "an empty index",
			entries:   nil,
			requested: "binlog.000002",
			wantErr:   errBinlogNotFound,
		},
		{
			name:      "a prefixed request does not match",
			entries:   []string{"binlog.000001", "binlog.000002"},
			requested: "./binlog.000002",
			wantErr:   errBinlogNotFound,
		},
		{
			name:      "a different basename",
			entries:   []string{"mysql-bin.000001", "mysql-bin.000002"},
			requested: "binlog.000002",
			wantErr:   errBinlogNotFound,
		},
		{
			name:      "duplicate entries start at the first match",
			entries:   []string{"binlog.000002", "binlog.000002", "binlog.000003"},
			requested: "binlog.000002",
			expected:  []string{"binlog.000002", "binlog.000002", "binlog.000003"},
		},
		{
			name:      "not a binary log name",
			entries:   []string{"binlog.000001"},
			requested: "binlog",
			wantErr:   errInvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandler(t)
			writeIndex(t, h, "binlog.index", tt.entries...)

			got, err := h.getBinlogsToStream(tt.requested)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
			assert.NotEmpty(t, got)
		})
	}

	t.Run("the index file is missing", func(t *testing.T) {
		h := newHandler(t)

		_, err := h.getBinlogsToStream("binlog.000002")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "open ")
		assert.NotErrorIs(t, err, errBinlogNotFound)
		assert.NotErrorIs(t, err, errInvalidRequest)
	})

	t.Run("the index is a directory", func(t *testing.T) {
		h := newHandler(t)
		require.NoError(t, os.Mkdir(filepath.Join(h.DataDir, "binlog.index"), 0o755))

		_, err := h.getBinlogsToStream("binlog.000002")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "read ")
	})
}

func TestCopyFirstBinlogToTar(t *testing.T) {
	// 14 bytes: the magic plus ten digits.
	const payload = "0123456789"

	tests := []struct {
		name         string
		position     int64
		expectedSize int64
		expected     string
		wantErr      string
	}{
		{name: "cuts at the position", position: 4, expectedSize: 10, expected: payload},
		{name: "cuts mid-payload", position: 8, expectedSize: 6, expected: "456789"},
		{
			name: "position at EOF", position: 14, expectedSize: 0, expected: "",
		},
		{
			name: "position 0 sends the magic too", position: 0, expectedSize: 14, expected: magic + payload,
		},
		{name: "past EOF", position: 15, wantErr: "is outside"},
		{name: "negative", position: -1, wantErr: "is outside"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandler(t)
			binlogFile(t, h, "binlog.000004", payload)

			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)

			err := h.copyFirstBinlogToTar("binlog.000004", tt.position, tw)

			if tt.wantErr != "" {
				require.ErrorIs(t, err, errInvalidRequest)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Zero(t, buf.Len(), "no bytes may reach the client")
				return
			}

			require.NoError(t, err)
			require.NoError(t, tw.Close())

			names, contents := tarEntries(t, buf.Bytes())
			assert.Equal(t, []string{"binlog.000004"}, names)
			assert.Equal(t, tt.expected, contents["binlog.000004"])
		})
	}

	t.Run("the entry name is always the basename", func(t *testing.T) {
		h := newHandler(t)
		binlogFile(t, h, "binlog.000004", payload)

		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)

		require.NoError(t, h.copyFirstBinlogToTar("/var/lib/mysql/binlog.000004", 4, tw))
		require.NoError(t, tw.Close())

		names, _ := tarEntries(t, buf.Bytes())
		assert.Equal(t, []string{"binlog.000004"}, names)
	})

	t.Run("the log is missing", func(t *testing.T) {
		h := newHandler(t)

		var buf bytes.Buffer
		err := h.copyFirstBinlogToTar("binlog.000004", 4, tar.NewWriter(&buf))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "open ")
		assert.Zero(t, buf.Len())
	})

	t.Run("an empty log", func(t *testing.T) {
		h := newHandler(t)
		require.NoError(t, os.WriteFile(filepath.Join(h.DataDir, "binlog.000004"), nil, 0o644))

		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)

		require.NoError(t, h.copyFirstBinlogToTar("binlog.000004", 0, tw))
		require.NoError(t, tw.Close())

		_, contents := tarEntries(t, buf.Bytes())
		assert.Equal(t, "", contents["binlog.000004"])
	})
}

func TestCopyBinlogToTar(t *testing.T) {
	t.Run("copies the whole log including the magic", func(t *testing.T) {
		h := newHandler(t)
		binlogFile(t, h, "binlog.000005", "whole-five")

		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)

		require.NoError(t, h.copyBinlogToTar("binlog.000005", tw))
		require.NoError(t, tw.Close())

		names, contents := tarEntries(t, buf.Bytes())
		assert.Equal(t, []string{"binlog.000005"}, names)
		assert.Equal(t, magic+"whole-five", contents["binlog.000005"])
	})

	t.Run("resolves a prefixed index entry", func(t *testing.T) {
		h := newHandler(t)
		binlogFile(t, h, "binlog.000005", "whole-five")

		for _, entry := range []string{"/var/lib/mysql/binlog.000005", "./binlog.000005"} {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)

			require.NoError(t, h.copyBinlogToTar(entry, tw))
			require.NoError(t, tw.Close())

			names, contents := tarEntries(t, buf.Bytes())
			assert.Equal(t, []string{"binlog.000005"}, names)
			assert.Equal(t, magic+"whole-five", contents["binlog.000005"])
		}
	})

	t.Run("appends in call order", func(t *testing.T) {
		h := newHandler(t)
		binlogFile(t, h, "binlog.000005", "whole-five")
		binlogFile(t, h, "binlog.000006", "whole-six")

		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)

		require.NoError(t, h.copyBinlogToTar("binlog.000005", tw))
		require.NoError(t, h.copyBinlogToTar("binlog.000006", tw))
		require.NoError(t, tw.Close())

		names, contents := tarEntries(t, buf.Bytes())
		assert.Equal(t, []string{"binlog.000005", "binlog.000006"}, names)
		assert.Equal(t, magic+"whole-six", contents["binlog.000006"])
	})

	t.Run("the log is missing", func(t *testing.T) {
		h := newHandler(t)

		var buf bytes.Buffer
		err := h.copyBinlogToTar("binlog.000005", tar.NewWriter(&buf))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "open ")
		assert.Zero(t, buf.Len())
	})

	t.Run("an empty log yields an empty entry", func(t *testing.T) {
		h := newHandler(t)
		require.NoError(t, os.WriteFile(filepath.Join(h.DataDir, "binlog.000006"), nil, 0o644))

		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)

		require.NoError(t, h.copyBinlogToTar("binlog.000006", tw))
		require.NoError(t, tw.Close())

		_, contents := tarEntries(t, buf.Bytes())
		assert.Equal(t, "", contents["binlog.000006"])
	})
}

func TestStreamBinlogs(t *testing.T) {
	t.Run("a later log failing aborts the stream", func(t *testing.T) {
		h := newHandler(t)
		binlogFile(t, h, "binlog.000002", "two")

		var buf bytes.Buffer
		err := h.streamBinlogs([]string{"binlog.000002", "binlog.000003"}, 4, tar.NewWriter(&buf))

		require.ErrorIs(t, err, errStreamAborted)
	})

	t.Run("a bad position does not abort the stream", func(t *testing.T) {
		h := newHandler(t)
		binlogFile(t, h, "binlog.000002", "two")

		var buf bytes.Buffer
		err := h.streamBinlogs([]string{"binlog.000002"}, 999, tar.NewWriter(&buf))

		require.ErrorIs(t, err, errInvalidRequest)
		assert.NotErrorIs(t, err, errStreamAborted)
		assert.Zero(t, buf.Len())
	})

	t.Run("a missing first log does not abort the stream", func(t *testing.T) {
		h := newHandler(t)

		var buf bytes.Buffer
		err := h.streamBinlogs([]string{"binlog.000002"}, 4, tar.NewWriter(&buf))

		require.Error(t, err)
		assert.NotErrorIs(t, err, errStreamAborted)
		assert.Zero(t, buf.Len())
	})
}

func threeBinlogs(t *testing.T) *FailoverHandler {
	t.Helper()

	h := newHandler(t)
	binlogFile(t, h, "binlog.000001", "one")
	binlogFile(t, h, "binlog.000002", "two")
	binlogFile(t, h, "binlog.000003", "three")
	writeIndex(t, h, "binlog.index", "./binlog.000001", "./binlog.000002", "./binlog.000003")

	return h
}

func streamRequest(t *testing.T, binlog string, position int64) []byte {
	t.Helper()

	body, err := json.Marshal(streamConfig{BinaryLog: binlog, Position: position})
	require.NoError(t, err)

	return body
}

func postStream(t *testing.T, h http.Handler, body []byte) (*http.Response, []byte, error) {
	t.Helper()

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Post(srv.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close() //nolint:errcheck

	data, readErr := io.ReadAll(resp.Body)

	return resp, data, readErr
}

func TestServeHTTP(t *testing.T) {
	t.Run("streams the requested log and everything after it", func(t *testing.T) {
		h := threeBinlogs(t)

		resp, body, err := postStream(t, h, streamRequest(t, "binlog.000002", 4))

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/x-tar", resp.Header.Get("Content-Type"))

		names, contents := tarEntries(t, body)
		assert.Equal(t, []string{"binlog.000002", "binlog.000003"}, names)
		assert.Equal(t, "two", contents["binlog.000002"], "the first log is cut at the position")
		assert.Equal(t, magic+"three", contents["binlog.000003"], "later logs are whole")
	})

	t.Run("the replica had already read the whole log", func(t *testing.T) {
		h := threeBinlogs(t)

		resp, body, err := postStream(t, h, streamRequest(t, "binlog.000003", int64(len(magic+"three"))))

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		names, contents := tarEntries(t, body)
		assert.Equal(t, []string{"binlog.000003"}, names)
		assert.Equal(t, "", contents["binlog.000003"])
	})

	requests := []struct {
		name       string
		body       []byte
		wantStatus int
		wantBody   string
	}{
		{
			name:       "malformed JSON",
			body:       []byte("{"),
			wantStatus: http.StatusBadRequest,
			wantBody:   "streaming failed\n",
		},
		{
			name:       "empty body",
			body:       nil,
			wantStatus: http.StatusBadRequest,
			wantBody:   "streaming failed\n",
		},
		{
			name:       "no binary log named",
			body:       []byte("{}"),
			wantStatus: http.StatusBadRequest,
			wantBody:   "invalid request\n",
		},
		{
			name:       "not a binary log name",
			body:       []byte(`{"binary_log":"binlog"}`),
			wantStatus: http.StatusBadRequest,
			wantBody:   "invalid request\n",
		},
	}

	for _, tt := range requests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body, err := postStream(t, threeBinlogs(t), tt.body)

			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			assert.Equal(t, tt.wantBody, string(body))
		})
	}

	t.Run("the requested log was purged", func(t *testing.T) {
		h := threeBinlogs(t)
		writeIndex(t, h, "binlog.index", "./binlog.000002", "./binlog.000003")

		resp, body, err := postStream(t, h, streamRequest(t, "binlog.000001", 4))

		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Equal(t, "requested binary log is not available\n", string(body))
	})

	for _, position := range []int64{999999, -1} {
		t.Run("position outside the log", func(t *testing.T) {
			resp, body, err := postStream(t, threeBinlogs(t), streamRequest(t, "binlog.000002", position))

			require.NoError(t, err)
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			assert.Equal(t, "invalid request\n", string(body))
		})
	}

	t.Run("the index is missing", func(t *testing.T) {
		resp, body, err := postStream(t, newHandler(t), streamRequest(t, "binlog.000002", 4))

		require.NoError(t, err)
		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		assert.Equal(t, "streaming failed\n", string(body))
	})

	t.Run("the first log is listed but missing", func(t *testing.T) {
		h := threeBinlogs(t)
		require.NoError(t, os.Remove(filepath.Join(h.DataDir, "binlog.000002")))

		resp, body, err := postStream(t, h, streamRequest(t, "binlog.000002", 4))

		require.NoError(t, err)
		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		assert.Equal(t, "streaming failed\n", string(body))
	})

	t.Run("a log that goes missing mid-stream aborts the transfer", func(t *testing.T) {
		h := threeBinlogs(t)
		require.NoError(t, os.Remove(filepath.Join(h.DataDir, "binlog.000003")))

		_, _, err := postStream(t, h, streamRequest(t, "binlog.000002", 4))

		require.Error(t, err)
	})
}
