package binlogsource

import (
	"context"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inUseBinlogIndex copies a binary log and marks it the file mysqld is currently
// writing, which is how the last file a failover source serves always looks on disk.
func inUseBinlogIndex(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", "binlog.000001"))
	require.NoError(t, err)

	// The flag lives in the header of the format description event, the first event in
	// the file. The trailer is left as it was, as mysqld leaves it.
	fde := raw[binlogFileHeaderSize:]
	fde = fde[:binary.LittleEndian.Uint32(fde[eventSizeOffset:])]

	flags := binary.LittleEndian.Uint16(fde[eventFlagsOffset:])
	binary.LittleEndian.PutUint16(fde[eventFlagsOffset:], flags|replication.LOG_EVENT_BINLOG_IN_USE_F)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "binlog.000001"), raw, 0o644))

	index := filepath.Join(dir, "binlog.index")
	require.NoError(t, os.WriteFile(index, []byte("./binlog.000001\n"), 0o644))

	return index
}

// streamAll returns every event the source serves a replica that has nothing.
func streamAll(t *testing.T, indexPath string) []*replication.BinlogEvent {
	t.Helper()

	srv := testSource(t, indexPath)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	empty := mysql.NewMysqlGTIDSet()
	streamer, err := newHandler(ctx, srv).HandleBinlogDumpGTID(&empty)
	require.NoError(t, err)

	var events []*replication.BinlogEvent
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		ev, err := streamer.GetEvent(ctx)
		cancel()
		if err != nil {
			// These tests ask for no heartbeats, so the stream goes quiet once
			// everything has been served.
			require.ErrorIs(t, err, context.DeadlineExceeded, "streaming failed")
			return events
		}
		events = append(events, ev)
	}
}

// A replica copies the format description event into its relay log. Left marked in
// use, it makes that log look like it was never closed and the replica truncates its
// tail the next time it starts.
func TestServedFormatDescriptionEventIsNotMarkedInUse(t *testing.T) {
	events := streamAll(t, inUseBinlogIndex(t))

	found := false
	for _, ev := range events {
		if ev.Header.EventType != replication.FORMAT_DESCRIPTION_EVENT {
			continue
		}
		found = true

		flags := binary.LittleEndian.Uint16(ev.RawData[eventFlagsOffset:])
		assert.Zero(t, flags&replication.LOG_EVENT_BINLOG_IN_USE_F, "format description event is still marked in use")

		covered := ev.RawData[:len(ev.RawData)-replication.BinlogChecksumLength]
		want := make([]byte, replication.BinlogChecksumLength)
		binary.LittleEndian.PutUint32(want, crc32.ChecksumIEEE(covered))
		assert.Equal(t, want, ev.RawData[len(covered):], "checksum was not recomputed after clearing the flag")
	}
	require.True(t, found, "no format description event was served")
}

// Every file is announced before its events, so the replica can record which of the
// source's logs it has reached. A real rotate event carries a timestamp; the
// artificial ones do not.
func TestEveryFileIsAnnouncedByARotateEvent(t *testing.T) {
	events := streamAll(t, filepath.Join("testdata", "binlog.index"))

	require.NotEmpty(t, events)
	assert.Equal(t, replication.ROTATE_EVENT, events[0].Header.EventType,
		"the stream must open with a rotate event, or the replica never learns a log name")

	announced := ""
	files := 0
	for _, ev := range events {
		switch ev.Header.EventType {
		case replication.ROTATE_EVENT:
			if ev.Header.Timestamp == 0 {
				announced = string(ev.Event.(*replication.RotateEvent).NextLogName)
			}
		case replication.FORMAT_DESCRIPTION_EVENT:
			files++
			assert.NotEmpty(t, announced, "a file's events arrived before the file was announced")
			announced = ""
		}
	}
	assert.Positive(t, files, "no file was streamed")
}

// copyIndex copies the fixture logs into a temporary directory, replacing the newest
// file's contents with whatever damage the caller wants.
func copyIndex(t *testing.T, damage func(t *testing.T, file string) []byte) string {
	t.Helper()

	files, err := readIndex(filepath.Join("testdata", "binlog.index"))
	require.NoError(t, err)

	dir := t.TempDir()
	names := make([]string, 0, len(files))
	for n, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		if n == len(files)-1 {
			raw = damage(t, f)
		}
		name := filepath.Base(f)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), raw, 0o644))
		names = append(names, "./"+name)
	}

	index := filepath.Join(dir, "binlog.index")
	require.NoError(t, os.WriteFile(index, []byte(strings.Join(names, "\n")+"\n"), 0o644))

	return index
}

// tornBinlogIndex takes the tail off the newest file's last event, so it claims more
// bytes than the file holds -- how a log mysqld is still writing can look when read.
func tornBinlogIndex(t *testing.T) string {
	t.Helper()

	return copyIndex(t, func(t *testing.T, file string) []byte {
		raw, err := os.ReadFile(file)
		require.NoError(t, err)
		return raw[:len(raw)-replication.BinlogChecksumLength]
	})
}

// A fragment must never reach the replica, and running into one ends the stream rather
// than failing it.
func TestATornFinalEventIsNotServed(t *testing.T) {
	whole := streamAll(t, filepath.Join("testdata", "binlog.index"))
	require.NotEmpty(t, whole)

	torn := streamAll(t, tornBinlogIndex(t))

	assert.Len(t, torn, len(whole)-1, "the stream should lose the fragment and nothing else")
}

// wrappedIndex rewrites the newest log's positions as mysqld writes them past 4GiB,
// where the four byte log_pos field has wrapped.
func wrappedIndex(t *testing.T) string {
	t.Helper()

	return copyIndex(t, func(t *testing.T, file string) []byte {
		raw, err := os.ReadFile(wrappedCopy(t, file))
		require.NoError(t, err)
		return raw
	})
}

func servedTypes(events []*replication.BinlogEvent) []replication.EventType {
	types := make([]replication.EventType, 0, len(events))
	for _, ev := range events {
		types = append(types, ev.Header.EventType)
	}
	return types
}

// Past 4GiB the positions in the headers are no guide to where a file ends, so how
// much of it to serve is accumulated from the event lengths. Framed by the headers
// instead, the stream would stop on the first event past the wrap.
func TestWrappedLogPosDoesNotCutTheStreamShort(t *testing.T) {
	whole := streamAll(t, filepath.Join("testdata", "binlog.index"))
	require.NotEmpty(t, whole)

	wrapped := streamAll(t, wrappedIndex(t))

	assert.Equal(t, servedTypes(whole), servedTypes(wrapped))
}

func cutIndex(t *testing.T) string {
	t.Helper()

	return copyIndex(t, func(t *testing.T, file string) []byte {
		cut, _ := uncommittedCopy(t, file)
		raw, err := os.ReadFile(cut)
		require.NoError(t, err)
		return raw
	})
}

func streamedGTIDs(t *testing.T, events []*replication.BinlogEvent) *mysql.MysqlGTIDSet {
	t.Helper()

	set := mysql.NewMysqlGTIDSet()
	for _, ev := range events {
		switch ev.Header.EventType {
		case replication.GTID_EVENT, replication.GTID_TAGGED_LOG_EVENT, replication.ANONYMOUS_GTID_EVENT:
		default:
			continue
		}

		g, err := gtidNext(ev.Header.EventType, eventBody(ev, true))
		require.NoError(t, err)
		set.AddGTIDWithTag(g.uuid, g.tag, g.gno)
	}

	return &set
}

// The endpoint tells the operator which transactions to wait for, so the stream has to
// carry exactly those. A transaction the old primary never committed is in neither.
func TestTheStreamCarriesExactlyWhatTheSourceAdvertises(t *testing.T) {
	index := cutIndex(t)

	files, err := readIndex(index)
	require.NoError(t, err)
	sc, err := scanBinlog(files[len(files)-1])
	require.NoError(t, err)

	want := sc.gtids.String()
	got := streamedGTIDs(t, streamAll(t, index))

	assert.Equal(t, want, got.String())
	assert.NotEmpty(t, want, "the fixture should advertise something")
}

func TestHeartbeatPeriodIsReadFromTheHandshake(t *testing.T) {
	for query, want := range map[string]time.Duration{
		"SET @master_heartbeat_period = 30000000000, @source_heartbeat_period = 30000000000": 30 * time.Second,
		"SET @source_heartbeat_period= 500000000":                                            500 * time.Millisecond,
		"SET @master_binlog_checksum = @@global.binlog_checksum":                             0,
		"SELECT @@global.gtid_executed":                                                      0,
	} {
		t.Run(query, func(t *testing.T) {
			h := newHandler(context.Background(), nil)
			h.noteHeartbeatPeriod(query)
			assert.Equal(t, want, time.Duration(h.heartbeat.Load()))
		})
	}
}

// With heartbeats negotiated the source holds the connection open instead of ending
// the dump, so the operator sees no replication error while the replica catches up.
func TestHeartbeatsHoldTheConnectionOpen(t *testing.T) {
	srv := testSource(t, filepath.Join("testdata", "binlog.index"))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	h := newHandler(ctx, srv)
	h.noteHeartbeatPeriod("SET @source_heartbeat_period = 50000000")

	empty := mysql.NewMysqlGTIDSet()
	streamer, err := h.HandleBinlogDumpGTID(&empty)
	require.NoError(t, err)

	deadline, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	for {
		ev, err := streamer.GetEvent(deadline)
		require.NoError(t, err, "the source ended the dump instead of holding it open")

		if ev.Header.EventType == replication.HEARTBEAT_EVENT {
			assert.Equal(t, srv.cfg.ServerID, ev.Header.ServerID)
			return
		}
	}
}

// Tearing the source down has to end a dump holding a connection open, or the
// goroutine and its connection outlive every failover.
func TestTearingDownTheSourceEndsAHeldOpenDump(t *testing.T) {
	srv := testSource(t, filepath.Join("testdata", "binlog.index"))

	ctx, cancel := context.WithCancel(context.Background())
	h := newHandler(ctx, srv)
	h.noteHeartbeatPeriod("SET @source_heartbeat_period = 3600000000000")

	empty := mysql.NewMysqlGTIDSet()
	streamer, err := h.HandleBinlogDumpGTID(&empty)
	require.NoError(t, err)

	cancel()

	deadline, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	for {
		if _, err := streamer.GetEvent(deadline); err != nil {
			require.ErrorIs(t, err, context.Canceled)
			return
		}
	}
}
