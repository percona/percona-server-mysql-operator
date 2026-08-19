package binlogsource

import (
	"encoding/binary"
	"hash/crc32"
	"path/filepath"

	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/pkg/errors"
)

// rotateHeaderLen is the fixed part of a rotate event body: the position it announces.
const rotateHeaderLen = 8

// binlogFileHeaderSize is the size of the magic bytes every binary log starts with.
const binlogFileHeaderSize = 4

// Field offsets within an event header.
const (
	eventSizeOffset   = 9
	eventLogPosOffset = 13
	eventFlagsOffset  = 17
)

// sanitizeFormatDescription clears LOG_EVENT_BINLOG_IN_USE_F, which mysqld leaves
// set on the log it is still writing. A replica copies the event into its relay log,
// and the flag there makes it truncate that log's tail the next time it starts.
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

// syntheticEvent assembles an event a source makes up rather than reads from a log.
// go-mysql only decodes events, so the bytes are laid out by hand. The timestamp is
// left at zero, as it is for every made-up event, and the caller fills in Event.
func syntheticEvent(
	evType replication.EventType,
	serverID uint32,
	logPos uint32,
	flags uint16,
	body []byte,
	checksum bool,
) *replication.BinlogEvent {
	size := replication.EventHeaderSize + len(body)
	if checksum {
		size += replication.BinlogChecksumLength
	}

	raw := make([]byte, size)
	raw[4] = byte(evType)
	binary.LittleEndian.PutUint32(raw[5:], serverID)
	binary.LittleEndian.PutUint32(raw[eventSizeOffset:], uint32(size))
	binary.LittleEndian.PutUint32(raw[eventLogPosOffset:], logPos)
	binary.LittleEndian.PutUint16(raw[eventFlagsOffset:], flags)
	copy(raw[replication.EventHeaderSize:], body)

	if checksum {
		setChecksum(raw)
	}

	return &replication.BinlogEvent{
		RawData: raw,
		Header: &replication.EventHeader{
			EventType: evType,
			ServerID:  serverID,
			EventSize: uint32(size),
			LogPos:    logPos,
			Flags:     flags,
		},
	}
}

// fakeRotateEvent builds the artificial rotate event a source sends ahead of every
// file it streams, so the replica learns the file's name. Without it the replica
// writes rotate events carrying an empty name into its relay log, and the applier
// stops with "Found invalid event in binary log".
//
// Mirrors Binlog_sender::fake_rotate_event: the zero timestamp is how a replica tells
// an artificial rotate from a real one, and log_pos zero stops it from advancing the
// source position it reports.
func fakeRotateEvent(serverID uint32, file string, pos uint64, checksum bool) *replication.BinlogEvent {
	// Only the base name travels; the replica has its own data directory.
	name := filepath.Base(file)

	body := make([]byte, rotateHeaderLen+len(name))
	binary.LittleEndian.PutUint64(body, pos)
	copy(body[rotateHeaderLen:], name)

	e := syntheticEvent(replication.ROTATE_EVENT, serverID, 0, replication.LOG_EVENT_ARTIFICIAL_F, body, checksum)

	// Only RawData goes on the wire, but an event on the stream has to be a whole one.
	e.Event = &replication.RotateEvent{Position: pos, NextLogName: []byte(name)}

	return e
}

// verifyChecksum checks an event against its CRC32 trailer, which covers the event's
// header as well as its body.
//
// LOG_EVENT_BINLOG_IN_USE_F comes off the format description event first: mysqld sets
// that flag on the log it is writing and clears it on close without touching the
// trailer, so the checksum only ever matches the event with the flag clear.
func verifyChecksum(e *replication.BinlogEvent) error {
	raw := e.RawData
	if len(raw) < replication.EventHeaderSize+replication.BinlogChecksumLength {
		return errors.Errorf("%s event is too short to carry a checksum", e.Header.EventType)
	}

	covered := raw[:len(raw)-replication.BinlogChecksumLength]
	if e.Header.EventType == replication.FORMAT_DESCRIPTION_EVENT &&
		e.Header.Flags&replication.LOG_EVENT_BINLOG_IN_USE_F != 0 {
		// On a copy: the event goes on to the replica as it was read.
		covered = append([]byte(nil), covered...)
		flags := binary.LittleEndian.Uint16(covered[eventFlagsOffset:])
		binary.LittleEndian.PutUint16(covered[eventFlagsOffset:], flags&^replication.LOG_EVENT_BINLOG_IN_USE_F)
	}

	want := binary.LittleEndian.Uint32(raw[len(raw)-replication.BinlogChecksumLength:])
	if got := crc32.ChecksumIEEE(covered); got != want {
		return errors.Errorf("%s event fails its checksum: %#08x, not the %#08x it carries",
			e.Header.EventType, got, want)
	}

	return nil
}

func setChecksum(raw []byte) {
	covered := raw[:len(raw)-replication.BinlogChecksumLength]
	binary.LittleEndian.PutUint32(raw[len(covered):], crc32.ChecksumIEEE(covered))
}

// heartbeatEvent builds the heartbeat a source sends while it has nothing else to
// send, so the replica does not decide the connection is dead. A replica asks for
// these with SET @source_heartbeat_period during the handshake.
//
// Mirrors Binlog_sender::send_heartbeat_event_v1: the body is the name of the log the
// source has reached and log_pos is how far into it. Version 2 goes only to a replica
// that asked for it in its dump flags, which go-mysql does not pass on.
func heartbeatEvent(serverID uint32, file string, pos int64, checksum bool) *replication.BinlogEvent {
	name := filepath.Base(file)

	// log_pos is four bytes wide, so past 4GiB this wraps as mysqld's own does.
	e := syntheticEvent(replication.HEARTBEAT_EVENT, serverID, uint32(pos), 0, []byte(name), checksum)
	e.Event = &replication.HeartbeatEvent{Version: 1, Filename: name}

	return e
}
