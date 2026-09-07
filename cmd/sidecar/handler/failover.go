package handler

import (
	"archive/tar"
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
)

const dataDir = "/var/lib/mysql"

type FailoverHandler struct {
	DataDir string
}

func FailoverStream() http.Handler {
	return &FailoverHandler{DataDir: dataDir}
}

type streamConfig struct {
	BinaryLog string `json:"binary_log"`
	Position  int64  `json:"position"`
}

var (
	errBinlogNotFound = errors.New("binary log not found")
	errInvalidRequest = errors.New("invalid request")
	errStreamAborted  = errors.New("stream aborted")
)

func (h *FailoverHandler) binlogPath(entry string) string {
	return path.Join(h.DataDir, path.Base(entry))
}

func (h *FailoverHandler) binlogIndexPath(requested string) (string, error) {
	basename := strings.TrimSuffix(requested, path.Ext(requested))
	if basename == "" || basename == requested {
		return "", fmt.Errorf("%w: %q is not a binary log name", errInvalidRequest, requested)
	}

	return h.binlogPath(basename + ".index"), nil
}

// getBinlogsToStream returns the binary logs the replica is missing: the one it
// asked for, plus everything rotated after it.
func (h *FailoverHandler) getBinlogsToStream(requested string) ([]string, error) {
	index, err := h.binlogIndexPath(requested)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(index)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", index, err)
	}
	defer f.Close() //nolint:errcheck

	binlogs := make([]string, 0)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		binlog := strings.TrimSpace(scanner.Text())
		if binlog == "" {
			continue
		}
		binlogs = append(binlogs, binlog)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", index, err)
	}

	for i, binlog := range binlogs {
		if path.Base(binlog) == requested {
			return binlogs[i:], nil
		}
	}

	return nil, fmt.Errorf("%w: %s", errBinlogNotFound, requested)
}

func (h *FailoverHandler) copyBinlogToTar(logName string, w *tar.Writer) error {
	f, err := os.Open(h.binlogPath(logName))
	if err != nil {
		return fmt.Errorf("open %s: %w", logName, err)
	}
	defer f.Close() //nolint:errcheck

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", logName, err)
	}

	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return fmt.Errorf("get file info header: %w", err)
	}
	hdr.Name = path.Base(logName)

	if err := w.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	written, err := io.Copy(w, f)
	if err != nil {
		return fmt.Errorf("copy %s to writer: %w", logName, err)
	}
	log.Printf("copied %d bytes", written)

	return nil
}

func (h *FailoverHandler) copyFirstBinlogToTar(logName string, position int64, w *tar.Writer) error {
	f, err := os.Open(h.binlogPath(logName))
	if err != nil {
		return fmt.Errorf("open %s: %w", logName, err)
	}
	defer f.Close() //nolint:errcheck

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", logName, err)
	}
	if position < 0 || position > fi.Size() {
		return fmt.Errorf("%w: position %d is outside %s (%d bytes)", errInvalidRequest, position, logName, fi.Size())
	}
	size := fi.Size() - position

	if err := w.WriteHeader(&tar.Header{Name: path.Base(logName), Size: size, Mode: 0644}); err != nil {
		return fmt.Errorf("%w: write header: %w", errStreamAborted, err)
	}

	if _, err := f.Seek(position, io.SeekStart); err != nil {
		return fmt.Errorf("%w: seek %s: %w", errStreamAborted, logName, err)
	}

	written, err := io.CopyN(w, f, size)
	if err != nil {
		return fmt.Errorf("%w: copy %s to writer: %w", errStreamAborted, logName, err)
	}
	log.Printf("copied %d bytes", written)

	return nil
}

func (h *FailoverHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	data, err := io.ReadAll(req.Body)
	if err != nil {
		log.Printf("ERROR: failed to read request body: %v", err)
		http.Error(w, "streaming failed", http.StatusBadRequest)
		return
	}
	defer req.Body.Close() //nolint:errcheck

	conf := streamConfig{}
	if err := json.Unmarshal(data, &conf); err != nil {
		log.Printf("ERROR: failed to unmarshal request body: %v", err)
		http.Error(w, "streaming failed", http.StatusBadRequest)
		return
	}

	binlogs, err := h.getBinlogsToStream(conf.BinaryLog)
	if err != nil {
		switch {
		case errors.Is(err, errBinlogNotFound):
			log.Printf("ERROR: %v", err)
			http.Error(w, "requested binary log is not available", http.StatusNotFound)
		case errors.Is(err, errInvalidRequest):
			log.Printf("ERROR: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
		default:
			log.Printf("ERROR: failed to get binlogs: %v", err)
			http.Error(w, "streaming failed", http.StatusInternalServerError)
		}
		return
	}

	if len(binlogs) == 0 {
		log.Printf("ERROR: no binary logs to stream for %s", conf.BinaryLog)
		http.Error(w, "streaming failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-tar")
	tw := tar.NewWriter(w)

	if err := h.streamBinlogs(binlogs, conf.Position, tw); err != nil {
		log.Printf("ERROR: failed to stream binary logs: %v", err)
		switch {
		case errors.Is(err, errStreamAborted):
			// a status written now would land inside the archive and the replica would
			// splice a truncated stream as if it were complete.
			panic(http.ErrAbortHandler)
		case errors.Is(err, errInvalidRequest):
			http.Error(w, "invalid request", http.StatusBadRequest)
		default:
			http.Error(w, "streaming failed", http.StatusInternalServerError)
		}
		return
	}

	if err := tw.Close(); err != nil {
		log.Printf("ERROR: failed to close tar writer: %v", err)
		panic(http.ErrAbortHandler)
	}
}

func (h *FailoverHandler) streamBinlogs(binlogs []string, position int64, tw *tar.Writer) error {
	// The first binary log is a special case: it's cut at the position the
	// replica had already received.
	if err := h.copyFirstBinlogToTar(binlogs[0], position, tw); err != nil {
		return fmt.Errorf("copy %s (pos %d): %w", binlogs[0], position, err)
	}

	for _, binlog := range binlogs[1:] {
		if err := h.copyBinlogToTar(binlog, tw); err != nil {
			return fmt.Errorf("%w: %w", errStreamAborted, err)
		}
	}

	return nil
}
