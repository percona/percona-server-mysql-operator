package binlogsource

import (
	"encoding/binary"
	"hash/crc32"
	"path/filepath"

	"github.com/go-mysql-org/go-mysql/replication"
)

// rotateHeaderLen is a rotate event's fixed part: the position in the file it
// announces.
const rotateHeaderLen = 8

// binlogFileHeaderSize is where a binary log's first event begins, past the
// magic bytes. Streaming a whole file starts there.
const binlogFileHeaderSize = 4

// Offsets of the fields we read or rewrite in an event header.
const (
	eventSizeOffset   = 9
	eventLogPosOffset = 13
	eventFlagsOffset  = 17
)

// sanitizeFormatDescription clears LOG_EVENT_BINLOG_IN_USE_F, which mysqld
// leaves set on the file it is still writing. A replica copies the event
// straight into its relay log, and the flag there marks that log as one that was
// never closed, so the replica truncates its tail the next time it starts.
//
// Binlog_sender::send_format_description_event clears the flag and leaves the
// checksum alone. We recompute it, so the event stays internally consistent.
func sanitizeFormatDescription(e *replication.BinlogEvent, checksum bool) {
	if e.Header.EventType != replication.FORMAT_DESCRIPTION_EVENT {
		return
	}

	flags := binary.LittleEndian.Uint16(e.RawData[eventFlagsOffset:])
	if flags&replication.LOG_EVENT_BINLOG_IN_USE_F == 0 {
		return
	}
	binary.LittleEndian.PutUint16(e.RawData[eventFlagsOffset:], flags&^replication.LOG_EVENT_BINLOG_IN_USE_F)
	e.Header.Flags = flags &^ replication.LOG_EVENT_BINLOG_IN_USE_F

	if checksum {
		setChecksum(e.RawData)
	}
}

// fakeRotateEvent builds the artificial rotate event a source sends ahead of
// every file it streams, so the replica learns the file's name.
//
// Without it the replica never learns a source log name at all. It writes
// rotate events of its own into the relay log to record where it is -- one for
// every PREVIOUS_GTIDS event it receives, among others -- and each carries the
// name it holds. An empty name yields a rotate event mysqld cannot read back,
// and the applier stops with "Found invalid event in binary log".
//
// Mirrors Binlog_sender::fake_rotate_event: the zero timestamp is how a replica
// tells an artificial rotate from a real one, and log_pos zero stops it from
// advancing the source position it reports.
func fakeRotateEvent(serverID uint32, file string, pos uint64, checksum bool) *replication.BinlogEvent {
	// Only the base name travels; the replica has its own data directory.
	name := filepath.Base(file)

	size := replication.EventHeaderSize + rotateHeaderLen + len(name)
	if checksum {
		size += replication.BinlogChecksumLength
	}

	// The timestamp and log_pos fields are left at zero.
	raw := make([]byte, size)
	raw[4] = byte(replication.ROTATE_EVENT)
	binary.LittleEndian.PutUint32(raw[5:], serverID)
	binary.LittleEndian.PutUint32(raw[eventSizeOffset:], uint32(size))
	binary.LittleEndian.PutUint16(raw[eventFlagsOffset:], replication.LOG_EVENT_ARTIFICIAL_F)

	binary.LittleEndian.PutUint64(raw[replication.EventHeaderSize:], pos)
	copy(raw[replication.EventHeaderSize+rotateHeaderLen:], name)

	if checksum {
		setChecksum(raw)
	}

	// Only RawData goes on the wire, but an event on the stream has to be a
	// whole one: TestFakeRotateEventDecodesToWhatItSays keeps the two in step.
	return &replication.BinlogEvent{
		RawData: raw,
		Header: &replication.EventHeader{
			EventType: replication.ROTATE_EVENT,
			ServerID:  serverID,
			EventSize: uint32(size),
			Flags:     replication.LOG_EVENT_ARTIFICIAL_F,
		},
		Event: &replication.RotateEvent{Position: pos, NextLogName: []byte(name)},
	}
}

// setChecksum rewrites the CRC32 trailer of an event over everything before it.
func setChecksum(raw []byte) {
	covered := raw[:len(raw)-replication.BinlogChecksumLength]
	binary.LittleEndian.PutUint32(raw[len(covered):], crc32.ChecksumIEEE(covered))
}

// heartbeatEvent builds the heartbeat a source sends while it has nothing else
// to send, so the replica does not decide the connection is dead. A replica asks
// for these with SET @source_heartbeat_period during the handshake.
//
// Mirrors Binlog_sender::send_heartbeat_event_v1: the body is the name of the
// log the source has reached, and log_pos is how far into it. Version 2 is only
// sent to a replica that asked for it in its dump flags, which go-mysql does not
// pass on, so version 1 it is.
func heartbeatEvent(serverID uint32, file string, pos uint32, checksum bool) *replication.BinlogEvent {
	name := filepath.Base(file)

	size := replication.EventHeaderSize + len(name)
	if checksum {
		size += replication.BinlogChecksumLength
	}

	// The timestamp field is left at zero, as it is for every made-up event.
	raw := make([]byte, size)
	raw[4] = byte(replication.HEARTBEAT_EVENT)
	binary.LittleEndian.PutUint32(raw[5:], serverID)
	binary.LittleEndian.PutUint32(raw[eventSizeOffset:], uint32(size))
	binary.LittleEndian.PutUint32(raw[eventLogPosOffset:], pos)
	copy(raw[replication.EventHeaderSize:], name)

	if checksum {
		setChecksum(raw)
	}

	return &replication.BinlogEvent{
		RawData: raw,
		Header: &replication.EventHeader{
			EventType: replication.HEARTBEAT_EVENT,
			ServerID:  serverID,
			EventSize: uint32(size),
			LogPos:    pos,
		},
		Event: &replication.GenericEvent{Data: raw[replication.EventHeaderSize:]},
	}
}
