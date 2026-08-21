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
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inUseBinlogIndex copies a binary log and marks it the file mysqld is
// currently writing, which is how every file a failover source serves last
// looks on disk. It returns the path of an index listing just that file.
func inUseBinlogIndex(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", "binlog.000001"))
	require.NoError(t, err)

	// The flag lives in the header of the format description event, the first
	// event in the file. mysqld computes the event's checksum with it set.
	fde := raw[binlogFileHeaderSize:]
	size := binary.LittleEndian.Uint32(fde[9:])
	fde = fde[:size]

	flags := binary.LittleEndian.Uint16(fde[eventFlagsOffset:])
	binary.LittleEndian.PutUint16(fde[eventFlagsOffset:], flags|replication.LOG_EVENT_BINLOG_IN_USE_F)
	covered := fde[:len(fde)-replication.BinlogChecksumLength]
	binary.LittleEndian.PutUint32(fde[len(covered):], crc32.ChecksumIEEE(covered))

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "binlog.000001"), raw, 0o644))

	index := filepath.Join(dir, "binlog.index")
	require.NoError(t, os.WriteFile(index, []byte("./binlog.000001\n"), 0o644))

	return index
}

// streamAll returns every event the source serves for a replica that has
// nothing, giving up once the stream goes quiet.
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
			// These tests do not ask for heartbeats, so the stream simply goes
			// quiet once everything has been served.
			require.ErrorIs(t, err, context.DeadlineExceeded, "streaming failed")
			return events
		}
		events = append(events, ev)
	}
}

// A replica copies the format description event into its relay log as it
// arrives. Left marked in use, it makes that relay log look like it was never
// closed, and the replica truncates its tail the next time it starts.
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

// Every file is announced before its events, so the replica can record which of
// the source's logs it has reached. A real rotate event carries a timestamp; the
// artificial ones a source synthesises do not.
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

// tornBinlogIndex copies the fixture logs and takes the tail off the newest
// file's last event, so it claims more bytes than the file holds. That is how a
// log mysqld is still writing can look when it is read.
func tornBinlogIndex(t *testing.T) string {
	t.Helper()

	idx, err := ReadIndex(filepath.Join("testdata", "binlog.index"))
	require.NoError(t, err)

	dir := t.TempDir()
	names := make([]string, 0, len(idx.Files))
	for n, f := range idx.Files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		if n == len(idx.Files)-1 {
			raw = raw[:len(raw)-replication.BinlogChecksumLength]
		}
		name := filepath.Base(f)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), raw, 0o644))
		names = append(names, "./"+name)
	}

	index := filepath.Join(dir, "binlog.index")
	require.NoError(t, os.WriteFile(index, []byte(strings.Join(names, "\n")+"\n"), 0o644))

	return index
}

// A fragment must never reach the replica, and running into one is the end of
// the stream rather than a failure to report.
func TestATornFinalEventIsNotServed(t *testing.T) {
	whole := streamAll(t, filepath.Join("testdata", "binlog.index"))
	require.NotEmpty(t, whole)

	torn := streamAll(t, tornBinlogIndex(t))

	assert.Len(t, torn, len(whole)-1, "the stream should lose the fragment and nothing else")
}

// cutIndex copies the fixture logs with the newest one's final transaction
// stripped of its commit, and returns the path of an index listing them.
func cutIndex(t *testing.T) string {
	t.Helper()

	idx, err := ReadIndex(filepath.Join("testdata", "binlog.index"))
	require.NoError(t, err)
	cut, _ := uncommittedCopy(t, idx.Files[len(idx.Files)-1])

	dir := t.TempDir()
	names := make([]string, 0, len(idx.Files))
	for n, f := range idx.Files {
		from := f
		if n == len(idx.Files)-1 {
			from = cut
		}
		raw, err := os.ReadFile(from)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, filepath.Base(f)), raw, 0o644))
		names = append(names, "./"+filepath.Base(f))
	}

	index := filepath.Join(dir, "binlog.index")
	require.NoError(t, os.WriteFile(index, []byte(strings.Join(names, "\n")+"\n"), 0o644))

	return index
}

// streamedGTIDs is every transaction the stream carried.
func streamedGTIDs(t *testing.T, events []*replication.BinlogEvent) *mysql.MysqlGTIDSet {
	t.Helper()

	set := mysql.NewMysqlGTIDSet()
	for _, ev := range events {
		if ev.Header.EventType != replication.GTID_EVENT {
			continue
		}

		body := ev.RawData[replication.EventHeaderSize : len(ev.RawData)-replication.BinlogChecksumLength]
		g := new(replication.GTIDEvent)
		require.NoError(t, g.Decode(body))

		u, err := uuid.FromBytes(g.SID)
		require.NoError(t, err)
		set.AddGTID(u, g.GNO)
	}

	return &set
}

// The endpoint tells the operator which transactions to wait for, so the stream
// has to carry exactly those. A transaction the old primary never committed is
// in neither.
func TestTheStreamCarriesExactlyWhatTheSourceAdvertises(t *testing.T) {
	index := cutIndex(t)

	idx, err := ReadIndex(index)
	require.NoError(t, err)
	want, err := ExecutedGTIDs(idx)
	require.NoError(t, err)

	got := streamedGTIDs(t, streamAll(t, index))

	assert.Equal(t, want.String(), got.String())
	assert.NotEmpty(t, want.String(), "the fixture should advertise something")
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

// With heartbeats negotiated the source holds the connection open instead of
// ending the dump, so the operator sees no replication error while it waits for
// the replica to apply what it has.
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

// Tearing the source down has to end a dump that is holding a connection open,
// or the goroutine and its connection outlive every failover.
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
