package main

import (
	"encoding/binary"
	"hash/crc32"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fde(serverID uint32, checksummed bool) []byte {
	const bodyLen = 57 // 2 version + 50 server version + 4 timestamp + 1 header length

	size := eventHeaderLen + bodyLen + 1 + checksumLen

	e := make([]byte, size)
	e[4] = fdeEventType
	binary.LittleEndian.PutUint32(e[5:9], serverID)
	binary.LittleEndian.PutUint32(e[9:13], uint32(size))
	binary.LittleEndian.PutUint32(e[13:17], uint32(size))
	binary.LittleEndian.PutUint16(e[19:21], 4)
	copy(e[21:71], "8.4.11-11")
	e[75] = eventHeaderLen

	if checksummed {
		e[size-checksumLen-1] = 1 // CRC32
	}
	binary.LittleEndian.PutUint32(e[size-checksumLen:], crc32.ChecksumIEEE(e[:size-checksumLen]))

	return e
}

func TestReadSourceHeader(t *testing.T) {
	t.Run("reads the server id and a CRC32 source", func(t *testing.T) {
		dir := t.TempDir()
		path := writeFile(t, dir, "binlog.000005", append([]byte(magic), fde(42, true)...))

		h, err := readSourceHeader(path)

		require.NoError(t, err)
		assert.Equal(t, uint32(42), h.serverID)
		assert.True(t, h.checksummed, "a matching trailing CRC32 means the source checksums its events")
	})

	t.Run("reads a source with checksums off", func(t *testing.T) {
		dir := t.TempDir()
		path := writeFile(t, dir, "binlog.000005", append([]byte(magic), fde(7, false)...))

		h, err := readSourceHeader(path)

		require.NoError(t, err)
		assert.Equal(t, uint32(7), h.serverID)
		assert.False(t, h.checksummed)
	})

	t.Run("rejects a file that does not start with a format description event", func(t *testing.T) {
		dir := t.TempDir()
		path := binlogFile(t, dir, "binlog.000005", "not-an-event-stream")

		_, err := readSourceHeader(path)

		require.ErrorIs(t, err, errNoFDE)
	})

	t.Run("rejects a file with no magic number", func(t *testing.T) {
		dir := t.TempDir()
		path := writeFile(t, dir, "binlog.000005", fde(42, true))

		_, err := readSourceHeader(path)

		require.ErrorIs(t, err, errNoFDE)
	})

	t.Run("rejects a truncated file", func(t *testing.T) {
		dir := t.TempDir()
		path := writeFile(t, dir, "binlog.000005", append([]byte(magic), fde(42, true)[:20]...))

		_, err := readSourceHeader(path)

		require.ErrorIs(t, err, errNoFDE)
	})

	t.Run("a missing file is a plain error", func(t *testing.T) {
		_, err := readSourceHeader(filepath.Join(t.TempDir(), "absent"))

		require.Error(t, err)
		assert.NotErrorIs(t, err, errNoFDE)
	})

	t.Run("rejects a header declaring an implausibly small event", func(t *testing.T) {
		dir := t.TempDir()
		e := fde(42, true)
		binary.LittleEndian.PutUint32(e[9:13], 20)
		path := writeFile(t, dir, "binlog.000005", append([]byte(magic), e...))

		_, err := readSourceHeader(path)

		require.ErrorIs(t, err, errNoFDE)
	})

	t.Run("rejects a header declaring an implausibly large event", func(t *testing.T) {
		dir := t.TempDir()
		e := fde(42, true)
		binary.LittleEndian.PutUint32(e[9:13], 1<<20)
		path := writeFile(t, dir, "binlog.000005", append([]byte(magic), e...))

		_, err := readSourceHeader(path)

		require.ErrorIs(t, err, errNoFDE)
	})
}

func TestRotateEvent(t *testing.T) {
	t.Run("carries the source's server id, an artificial flag and position 4", func(t *testing.T) {
		e := rotateEvent("binlog.000005", sourceHeader{serverID: 42, checksummed: true})

		require.Len(t, e, eventHeaderLen+8+len("binlog.000005")+checksumLen)
		assert.Zero(t, binary.LittleEndian.Uint32(e[0:4]), "an artificial event carries no timestamp")
		assert.Equal(t, byte(4), e[4])
		assert.Equal(t, uint32(42), binary.LittleEndian.Uint32(e[5:9]),
			"the source's id, so the replica does not filter the event as its own")
		assert.Equal(t, uint32(len(e)), binary.LittleEndian.Uint32(e[9:13]))
		assert.Zero(t, binary.LittleEndian.Uint32(e[13:17]), "an artificial event carries no next position")
		assert.Equal(t, uint16(0x0020), binary.LittleEndian.Uint16(e[17:19]))
		assert.Equal(t, uint64(4), binary.LittleEndian.Uint64(e[19:27]))
		assert.Equal(t, "binlog.000005", string(e[27:len(e)-checksumLen]),
			"the new log name is not null-terminated")
		assert.Equal(t, crc32.ChecksumIEEE(e[:len(e)-checksumLen]),
			binary.LittleEndian.Uint32(e[len(e)-checksumLen:]))
	})

	t.Run("omits the checksum when the source does not use one", func(t *testing.T) {
		e := rotateEvent("binlog.000005", sourceHeader{serverID: 42})

		require.Len(t, e, eventHeaderLen+8+len("binlog.000005"))
		assert.Equal(t, "binlog.000005", string(e[27:]))
	})
}
