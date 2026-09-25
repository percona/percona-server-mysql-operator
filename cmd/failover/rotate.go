package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"os"
	"path/filepath"
)

const (
	eventHeaderLen = 19
	checksumLen    = 4

	rotateEventType = 4
	fdeEventType    = 15

	// Marks an event the replica's receiver synthesized rather than one the source wrote.
	logEventArtificialF = 0x0020

	binlogStartPos = 4

	maxFDESize = 1 << 16
)

var errNoFDE = errors.New("no format description event")

type sourceHeader struct {
	serverID    uint32
	checksummed bool
}

func readSourceHeader(path string) (sourceHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return sourceHeader{}, err
	}
	defer f.Close() //nolint:errcheck

	name := filepath.Base(path)

	if err := readMagic(f); err != nil {
		return sourceHeader{}, fmt.Errorf("%w: %s: %w", errNoFDE, name, err)
	}

	header := make([]byte, eventHeaderLen)
	if _, err := io.ReadFull(f, header); err != nil {
		return sourceHeader{}, fmt.Errorf("%w: %s is shorter than one event: %w", errNoFDE, name, err)
	}
	if header[4] != fdeEventType {
		return sourceHeader{}, fmt.Errorf("%w: %s starts with event type %d", errNoFDE, name, header[4])
	}

	size := binary.LittleEndian.Uint32(header[9:13])
	if size < eventHeaderLen+1+checksumLen || size > maxFDESize {
		return sourceHeader{}, fmt.Errorf("%w: %s declares a %d byte event", errNoFDE, name, size)
	}

	event := make([]byte, size)
	copy(event, header)
	if _, err := io.ReadFull(f, event[eventHeaderLen:]); err != nil {
		return sourceHeader{}, fmt.Errorf("%w: %s is truncated inside its first event: %w", errNoFDE, name, err)
	}

	return sourceHeader{
		serverID:    binary.LittleEndian.Uint32(header[5:9]),
		checksummed: event[size-checksumLen-1] == 1,
	}, nil
}

func rotateEvent(logName string, h sourceHeader) []byte {
	size := eventHeaderLen + 8 + len(logName)
	if h.checksummed {
		size += checksumLen
	}

	e := make([]byte, size)
	e[4] = rotateEventType
	binary.LittleEndian.PutUint32(e[5:9], h.serverID)
	binary.LittleEndian.PutUint32(e[9:13], uint32(size))
	binary.LittleEndian.PutUint16(e[17:19], logEventArtificialF)
	binary.LittleEndian.PutUint64(e[eventHeaderLen:], binlogStartPos)
	copy(e[eventHeaderLen+8:], logName)

	if h.checksummed {
		binary.LittleEndian.PutUint32(e[size-checksumLen:], crc32.ChecksumIEEE(e[:size-checksumLen]))
	}

	return e
}

func appendRotate(w io.Writer, sourceLog string) error {
	name := filepath.Base(sourceLog)

	h, err := readSourceHeader(sourceLog)
	if err != nil {
		if errors.Is(err, errNoFDE) {
			log.Printf("WARNING: appending %s without a Rotate event: %v", name, err)
			return nil
		}

		return err
	}

	n, err := w.Write(rotateEvent(name, h))
	if err != nil {
		return err
	}

	log.Printf("written %d bytes of Rotate event for %s", n, name)

	return nil
}
