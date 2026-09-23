package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

var errTornBinlog = errors.New("binary log ends inside an event")

// truncateTornTail cuts every staged log back to the end of its last complete
// event. A source that dies mid-write leaves a partial event behind, and the
// applier rejects the whole relay log once it reads one.
func truncateTornTail(sourceLogs []string) error {
	for i, path := range sourceLogs {
		// The first log is cut at the position the replica had already read, which
		// is an event boundary. Every later one is a whole file behind a magic number.
		var start int64
		if i > 0 {
			start = binlogStartPos
		}

		end, size, err := lastEventEnd(path, start)
		if err != nil {
			return err
		}

		torn := size - end
		if torn == 0 {
			continue
		}

		name := filepath.Base(path)
		if i < len(sourceLogs)-1 {
			return fmt.Errorf("%w: %s has %d byte(s) after its last complete event", errTornBinlog, name, torn)
		}

		// The source died writing these, so they are a transaction it never
		// committed and never sent anywhere.
		log.Printf("WARNING: dropping %d byte(s) of a partial event from the end of %s", torn, name)

		if err := os.Truncate(path, end); err != nil {
			return fmt.Errorf("truncate %s: %w", name, err)
		}
	}

	return nil
}

// lastEventEnd returns the offset at which the last complete event in path
// ends, along with the file's size. Events begin at start, which skips the
// magic number a whole binary log carries.
func lastEventEnd(path string, start int64) (end, size int64, err error) {
	name := filepath.Base(path)

	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close() //nolint:errcheck

	fi, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	size = fi.Size()
	if size < start {
		return 0, 0, fmt.Errorf("%s is %d byte(s), shorter than a binary log header", name, size)
	}

	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return 0, 0, err
	}

	r := bufio.NewReader(f)
	header := make([]byte, eventHeaderLen)
	end = start

	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return end, size, nil
			}

			return 0, 0, fmt.Errorf("read %s: %w", name, err)
		}

		eventSize := int64(binary.LittleEndian.Uint32(header[9:13]))
		if eventSize < eventHeaderLen || end+eventSize > size {
			return end, size, nil
		}

		if _, err := io.CopyN(io.Discard, r, eventSize-eventHeaderLen); err != nil {
			return 0, 0, fmt.Errorf("read %s: %w", name, err)
		}

		end += eventSize
	}
}
