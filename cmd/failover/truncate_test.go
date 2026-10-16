package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/percona-server-mysql-operator/pkg/mysql/gtid"
)

// eventOfLength builds an event whose header declares a length the body does not
// match, which is what a partial write leaves behind.
func eventOfLength(payload string, declared uint32) string {
	e := []byte(binlogEvent(payload))
	binary.LittleEndian.PutUint32(e[9:13], declared)

	return string(e)
}

// tornEvent is an event whose last bytes never made it to disk.
func tornEvent(payload string) string {
	e := binlogEvent(payload)

	return e[:len(e)-3]
}

// stagedLogs writes what fetchLogsFromSource leaves in the staging dir: the first
// log cut at the position the replica had already read, every later one whole.
func stagedLogs(t *testing.T, contents ...string) []string {
	t.Helper()

	dir := t.TempDir()
	logs := make([]string, 0, len(contents))

	for i, content := range contents {
		if i > 0 {
			content = magic + content
		}
		logs = append(logs, writeFile(t, dir, fmt.Sprintf("binlog.%06d", 4+i), []byte(content)))
	}

	return logs
}

// typedEvent is binlogEvent with an event type.
func typedEvent(eventType byte, payload string) string {
	e := []byte(binlogEvent(payload))
	e[4] = eventType

	return string(e)
}

func requireTrimmed(t *testing.T, logs []string, wantGTIDs string) {
	t.Helper()

	gtids, err := trimSourceLogs(logs, nil)

	require.NoError(t, err)
	assert.Equal(t, wantGTIDs, gtids)
}

func readAll(t *testing.T, paths []string) []string {
	t.Helper()

	contents := make([]string, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		contents = append(contents, string(b))
	}

	return contents
}

func TestScanLog(t *testing.T) {
	one := binlogEvent("one")
	two := binlogEvent("two")

	tests := map[string]struct {
		content string
		start   int64
		want    int64
	}{
		"a whole log walks to the end": {
			content: magic + one + two,
			start:   binlogStartPos,
			want:    int64(len(magic + one + two)),
		},
		"a log cut at a read position carries no magic number": {
			content: one + two,
			start:   0,
			want:    int64(len(one + two)),
		},
		"a body cut short stops at the last whole event": {
			content: magic + one + two + tornEvent("three"),
			start:   binlogStartPos,
			want:    int64(len(magic + one + two)),
		},
		"a header cut short stops at the last whole event": {
			content: magic + one + two + binlogEvent("three")[:10],
			start:   binlogStartPos,
			want:    int64(len(magic + one + two)),
		},
		"a length of zero stops the walk": {
			content: magic + one + eventOfLength("three", 0),
			start:   binlogStartPos,
			want:    int64(len(magic + one)),
		},
		"a length shorter than a header stops the walk": {
			content: magic + one + eventOfLength("three", eventHeaderLen-1),
			start:   binlogStartPos,
			want:    int64(len(magic + one)),
		},
		"an event with no body is walked past": {
			content: magic + one + binlogEvent("") + two,
			start:   binlogStartPos,
			want:    int64(len(magic + one + binlogEvent("") + two)),
		},
		"a length past the end of the file stops the walk": {
			content: magic + one + eventOfLength("three", 1<<20),
			start:   binlogStartPos,
			want:    int64(len(magic + one)),
		},
		"a log the replica had read to the end is empty": {
			content: "",
			start:   0,
			want:    0,
		},
		"nothing but a magic number": {
			content: magic,
			start:   binlogStartPos,
			want:    binlogStartPos,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeFile(t, t.TempDir(), "binlog.000004", []byte(tt.content))

			scan, err := scanLog(path, tt.start, nil)

			require.NoError(t, err)
			assert.Equal(t, tt.want, scan.eventsEnd)
		})
	}

	t.Run("a log shorter than its magic number", func(t *testing.T) {
		path := writeFile(t, t.TempDir(), "binlog.000005", []byte("\xfeb"))

		_, err := scanLog(path, binlogStartPos, nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "shorter than a binary log header")
	})

	for _, b := range realBinlogs {
		t.Run(b.name+": collects the GTIDs of the complete transactions", func(t *testing.T) {
			path := writeFile(t, t.TempDir(), "binlog.000004", []byte(b.read(t)))

			scan, err := scanLog(path, binlogStartPos, nil)

			require.NoError(t, err)
			assert.Equal(t, b.gtids, scan.gtids.String())
			assert.Nil(t, scan.openTx)
		})

		t.Run(b.name+": leaves out the open transaction", func(t *testing.T) {
			content := b.read(t)
			path := writeFile(t, t.TempDir(), "binlog.000004", []byte(content[:b.largeTxAt+b.largeTxLen/2]))

			scan, err := scanLog(path, binlogStartPos, nil)

			require.NoError(t, err)
			assert.Equal(t, b.gtidsBeforeLargeTx, scan.gtids.String())
			require.NotNil(t, scan.openTx)
			assert.Equal(t, uint64(b.largeTxLen), scan.openTx.txLen)
		})
	}

	t.Run("counts the carried transaction once it is finished", func(t *testing.T) {
		b := realBinlogs[1]
		content := b.read(t)
		gtidEnd := eventEnd(content, b.largeTxAt)
		relayLogs := relayLogsOf(t, content[:gtidEnd])
		carried, err := receivedPart(relayLogs, relayLogs[0], uint64(gtidEnd))
		require.NoError(t, err)
		path := writeFile(t, t.TempDir(), "binlog.000004", []byte(content[gtidEnd:]))

		scan, err := scanLog(path, 0, carried)

		require.NoError(t, err)
		assert.Equal(t, fromLargeTx(t, b), scan.gtids.String())
		assert.Nil(t, scan.openTx)
	})

	t.Run("leaves out the carried transaction while it is open", func(t *testing.T) {
		b := realBinlogs[1]
		content := b.read(t)
		gtidEnd := eventEnd(content, b.largeTxAt)
		relayLogs := relayLogsOf(t, content[:gtidEnd])
		carried, err := receivedPart(relayLogs, relayLogs[0], uint64(gtidEnd))
		require.NoError(t, err)
		path := writeFile(t, t.TempDir(), "binlog.000004", []byte(content[gtidEnd:b.largeTxAt+b.largeTxLen/2]))

		scan, err := scanLog(path, 0, carried)

		require.NoError(t, err)
		assert.True(t, scan.gtids.IsEmpty())
		assert.Equal(t, &carried.gtidEvent, scan.openTx)
	})

	t.Run("a missing log", func(t *testing.T) {
		_, err := scanLog(filepath.Join(t.TempDir(), "binlog.000004"), 0, nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such file")
	})
}

func TestTrimSourceLogs(t *testing.T) {
	t.Run("leaves whole logs alone", func(t *testing.T) {
		logs := stagedLogs(t, binlogEvent("four-tail"), binlogEvent("whole-five"))
		before := readAll(t, logs)

		requireTrimmed(t, logs, "")

		assert.Equal(t, before, readAll(t, logs))
	})

	t.Run("cuts a partial event from the newest log", func(t *testing.T) {
		logs := stagedLogs(t,
			binlogEvent("four-tail"),
			binlogEvent("whole-five")+tornEvent("half-five"),
		)

		requireTrimmed(t, logs, "")

		assert.Equal(t, []string{
			binlogEvent("four-tail"),
			magic + binlogEvent("whole-five"),
		}, readAll(t, logs))
	})

	t.Run("cuts a partial event from a lone log", func(t *testing.T) {
		logs := stagedLogs(t, binlogEvent("four-tail")+tornEvent("half-four"))

		requireTrimmed(t, logs, "")

		assert.Equal(t, []string{binlogEvent("four-tail")}, readAll(t, logs))
	})

	t.Run("a newest log that is nothing but a partial event", func(t *testing.T) {
		logs := stagedLogs(t, tornEvent("half-four"))

		requireTrimmed(t, logs, "")

		assert.Equal(t, []string{""}, readAll(t, logs),
			"an empty staged log is what a caught-up replica already gets")
	})

	t.Run("refuses a partial event in an earlier log", func(t *testing.T) {
		logs := stagedLogs(t,
			binlogEvent("four-tail"),
			binlogEvent("whole-five")+tornEvent("half-five"),
			binlogEvent("whole-six"),
		)
		before := readAll(t, logs)

		_, err := trimSourceLogs(logs, nil)

		require.ErrorIs(t, err, errTornBinlog)
		assert.Contains(t, err.Error(), "binlog.000005")
		assert.Equal(t, before, readAll(t, logs), "no log may be cut when the splice is refused")
	})

	t.Run("refuses a partial event in the first of several logs", func(t *testing.T) {
		logs := stagedLogs(t, tornEvent("half-four"), binlogEvent("whole-five"))
		before := readAll(t, logs)

		_, err := trimSourceLogs(logs, nil)

		require.ErrorIs(t, err, errTornBinlog)
		assert.Equal(t, before, readAll(t, logs))
	})

	t.Run("a first log the replica had read to the end is not torn", func(t *testing.T) {
		logs := stagedLogs(t, "", binlogEvent("whole-five"))
		before := readAll(t, logs)

		requireTrimmed(t, logs, "")

		assert.Equal(t, before, readAll(t, logs))
	})

	for _, b := range realBinlogs {
		t.Run(b.name+": collects the GTIDs and drops the Stop event", func(t *testing.T) {
			content := b.read(t)
			logs := stagedLogs(t, "", content[binlogStartPos:])

			requireTrimmed(t, logs, b.gtids)

			assert.Equal(t, []string{"", content[:b.stopAt]}, readAll(t, logs))
		})

		t.Run(b.name+": keeps a last transaction that ends the log", func(t *testing.T) {
			content := b.read(t)
			logs := stagedLogs(t, content[binlogStartPos:b.stopAt])

			requireTrimmed(t, logs, b.gtids)

			assert.Equal(t, []string{content[binlogStartPos:b.stopAt]}, readAll(t, logs))
		})

		t.Run(b.name+": cuts a transaction torn inside an event", func(t *testing.T) {
			content := b.read(t)
			logs := stagedLogs(t, content[:b.largeTxAt+b.largeTxLen/2][binlogStartPos:])

			requireTrimmed(t, logs, b.gtidsBeforeLargeTx)

			assert.Equal(t, []string{content[binlogStartPos:b.largeTxAt]}, readAll(t, logs))
		})
	}

	t.Run("cuts a transaction torn at an event boundary", func(t *testing.T) {
		b := realBinlogs[1]
		content := b.read(t)
		gtidEnd := eventEnd(content, b.largeTxAt)
		logs := stagedLogs(t, "", content[binlogStartPos:gtidEnd])

		requireTrimmed(t, logs, b.gtidsBeforeLargeTx)

		assert.Equal(t, []string{"", content[:b.largeTxAt]}, readAll(t, logs))
	})

	t.Run("a source that only shut down sent nothing to apply", func(t *testing.T) {
		b := realBinlogs[1]
		logs := stagedLogs(t, b.read(t)[b.stopAt:])

		requireTrimmed(t, logs, "")

		assert.Equal(t, []string{""}, readAll(t, logs))
	})

	t.Run("drops the Stop event of a log the source restarted after", func(t *testing.T) {
		first, second := realBinlogs[1], realBinlogs[2]
		logs := stagedLogs(t, first.read(t)[binlogStartPos:], second.read(t)[binlogStartPos:])

		// the set is sorted by source uuid, which puts 9.7's before 8.4's
		requireTrimmed(t, logs, second.gtids+","+first.gtids)

		assert.Equal(t, []string{
			first.read(t)[binlogStartPos:first.stopAt],
			second.read(t)[:second.stopAt],
		}, readAll(t, logs))
	})

	t.Run("refuses a torn transaction in an earlier log", func(t *testing.T) {
		b := realBinlogs[1]
		content := b.read(t)
		logs := stagedLogs(t, content[binlogStartPos:b.largeTxAt+b.largeTxLen/2], binlogEvent("whole-five"))
		before := readAll(t, logs)

		_, err := trimSourceLogs(logs, nil)

		require.ErrorIs(t, err, errTornBinlog)
		assert.Contains(t, err.Error(), "binlog.000004")
		assert.Equal(t, before, readAll(t, logs), "no log may be cut when the splice is refused")
	})

	t.Run("a Stop event that is not the last event is left alone", func(t *testing.T) {
		logs := stagedLogs(t, typedEvent(stopEventType, "")+binlogEvent("four-tail"))
		before := readAll(t, logs)

		requireTrimmed(t, logs, "")

		assert.Equal(t, before, readAll(t, logs))
	})

	t.Run("refuses an anonymous GTID", func(t *testing.T) {
		logs := stagedLogs(t, binlogEvent("four-tail")+typedEvent(anonymousGTIDEventType, "gtid"))

		_, err := trimSourceLogs(logs, nil)

		require.ErrorIs(t, err, errUnsupportedGTID)
		assert.Contains(t, err.Error(), "binlog.000004")
	})

	t.Run("refuses a malformed tagged GTID", func(t *testing.T) {
		logs := stagedLogs(t, binlogEvent("four-tail")+typedEvent(taggedGTIDEventType, "gtid"))

		_, err := trimSourceLogs(logs, nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "binlog.000004 at 28")
	})

	t.Run("no logs", func(t *testing.T) {
		requireTrimmed(t, nil, "")
	})

	t.Run("a missing log", func(t *testing.T) {
		_, err := trimSourceLogs([]string{filepath.Join(t.TempDir(), "binlog.000004")}, nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such file")
	})
}

// eventEnd is where the event at offset at ends
func eventEnd(content string, at int) int {
	return at + int(binary.LittleEndian.Uint32([]byte(content[at+9:at+13])))
}

// relayRotate is the Rotate event FLUSH RELAY LOGS closes a relay log with
func relayRotate(next string) string {
	e := rotateEvent(next, sourceHeader{serverID: 2})
	binary.LittleEndian.PutUint16(e[17:19], logEventRelayLogF)

	return string(e)
}

// relayLogsOf lays out relay logs with the given contents, the first being the
// applier's, each closed by a relay Rotate, followed by the empty one FLUSH
// RELAY LOGS opens
func relayLogsOf(t *testing.T, contents ...string) relayIndex {
	t.Helper()

	dir := t.TempDir()
	logs := make(relayIndex, 0, len(contents)+1)

	for i, content := range append(contents, magic) {
		name := fmt.Sprintf("relay-bin.%06d", i+1)
		if i < len(contents) {
			content += relayRotate(fmt.Sprintf("relay-bin.%06d", i+2))
		}
		logs = append(logs, writeFile(t, dir, name, []byte(content)))
	}

	return logs
}

// lastEventAt is where the last event of the large transaction starts
func lastEventAt(content string, b realBinlog) int {
	at := b.largeTxAt
	for eventEnd(content, at) < b.largeTxAt+b.largeTxLen {
		at = eventEnd(content, at)
	}

	return at
}

func TestReceivedPart(t *testing.T) {
	for _, b := range realBinlogs {
		t.Run(b.name+": the relay log ends after a GTID event", func(t *testing.T) {
			content := b.read(t)
			gtidEnd := eventEnd(content, b.largeTxAt)
			relayLogs := relayLogsOf(t, content[:gtidEnd])

			carried, err := receivedPart(relayLogs, relayLogs[0], uint64(gtidEnd))

			require.NoError(t, err)
			require.NotNil(t, carried)
			assert.Equal(t, int64(gtidEnd-b.largeTxAt), carried.received)
			assert.Equal(t, uint64(b.largeTxLen), carried.txLen)
		})

		t.Run(b.name+": the relay log ends before the last event of a transaction", func(t *testing.T) {
			content := b.read(t)
			lastAt := lastEventAt(content, b)
			relayLogs := relayLogsOf(t, content[:lastAt])

			carried, err := receivedPart(relayLogs, relayLogs[0], uint64(lastAt))

			require.NoError(t, err)
			require.NotNil(t, carried)
			assert.Equal(t, int64(lastAt-b.largeTxAt), carried.received)
		})
	}

	t.Run("a transaction a rotation split across relay logs", func(t *testing.T) {
		b := realBinlogs[1]
		content := b.read(t)
		gtidEnd := eventEnd(content, b.largeTxAt)
		nextEnd := eventEnd(content, gtidEnd)
		relayLogs := relayLogsOf(t, content[:gtidEnd], magic+content[gtidEnd:nextEnd])

		carried, err := receivedPart(relayLogs, relayLogs[0], uint64(nextEnd))

		require.NoError(t, err)
		require.NotNil(t, carried)
		assert.Equal(t, int64(nextEnd-b.largeTxAt), carried.received)
	})

	t.Run("a whole transaction a rotation split across relay logs", func(t *testing.T) {
		b := realBinlogs[1]
		content := b.read(t)
		gtidEnd := eventEnd(content, b.largeTxAt)
		txEnd := b.largeTxAt + b.largeTxLen
		relayLogs := relayLogsOf(t, content[:gtidEnd], magic+content[gtidEnd:txEnd])

		carried, err := receivedPart(relayLogs, relayLogs[0], uint64(txEnd))

		require.NoError(t, err)
		assert.Nil(t, carried)
	})

	t.Run("a relay log that ends between transactions", func(t *testing.T) {
		b := realBinlogs[1]
		content := b.read(t)
		relayLogs := relayLogsOf(t, content[:b.largeTxAt])

		carried, err := receivedPart(relayLogs, relayLogs[0], uint64(b.largeTxAt))

		require.NoError(t, err)
		assert.Nil(t, carried)
	})

	t.Run("a source that moved to its next log after the transaction", func(t *testing.T) {
		b := realBinlogs[1]
		content := b.read(t)
		txEnd := b.largeTxAt + b.largeTxLen
		next := string(rotateEvent("binlog.000005", sourceHeader{serverID: 1}))
		relayLogs := relayLogsOf(t, content[:txEnd]+next, magic)

		carried, err := receivedPart(relayLogs, relayLogs[0], uint64(eventEnd(content, b.largeTxAt)))

		require.NoError(t, err)
		assert.Nil(t, carried)
	})

	t.Run("a source log past 4GiB", func(t *testing.T) {
		b := realBinlogs[1]
		content := b.read(t)
		gtidEnd := eventEnd(content, b.largeTxAt)
		relayLogs := relayLogsOf(t, content[:gtidEnd])

		carried, err := receivedPart(relayLogs, relayLogs[0], 1<<32+uint64(gtidEnd))

		require.NoError(t, err)
		require.NotNil(t, carried)
		assert.Equal(t, int64(gtidEnd-b.largeTxAt), carried.received)
	})

	t.Run("an anonymous transaction", func(t *testing.T) {
		relayLogs := relayLogsOf(t, magic+typedEvent(anonymousGTIDEventType, "anonymous"))

		carried, err := receivedPart(relayLogs, relayLogs[0], 1000)

		require.NoError(t, err)
		assert.Nil(t, carried)
	})

	t.Run("relay logs that hold no transaction", func(t *testing.T) {
		relayLogs := relayLogsOf(t, magic+binlogEvent("relay-one"), magic+binlogEvent("relay-two"))

		carried, err := receivedPart(relayLogs, relayLogs[0], 1000)

		require.NoError(t, err)
		assert.Nil(t, carried)
	})

	t.Run("refuses an index with no closed relay log", func(t *testing.T) {
		relayLogs := relayIndex{writeFile(t, t.TempDir(), "relay-bin.000001", []byte(magic))}

		_, err := receivedPart(relayLogs, relayLogs[0], 1000)

		require.EqualError(t, err, "the index lists 1 relay log(s), so no relay log is closed and none can be appended to")
	})

	t.Run("refuses an applier log the index does not list", func(t *testing.T) {
		relayLogs := relayLogsOf(t, magic)

		_, err := receivedPart(relayLogs, "relay-bin.000009", 1000)

		require.EqualError(t, err, `the index does not list the applier's relay log "relay-bin.000009"`)
	})

	t.Run("refuses a read position inside the GTID event", func(t *testing.T) {
		b := realBinlogs[1]
		content := b.read(t)
		relayLogs := relayLogsOf(t, content[:eventEnd(content, b.largeTxAt)])

		_, err := receivedPart(relayLogs, relayLogs[0], uint64(b.largeTxAt+1))

		require.EqualError(t, err, fmt.Sprintf(
			"relay-bin.000001 ends inside transaction ee61b2b7-c64f-11f1-b6f6-a8a159366a35:5, which starts at source position %d, but the receiver read up to %d, short of its GTID event",
			b.largeTxAt, b.largeTxAt+1))
	})
}

// fromLargeTx is the GTIDs of the large transaction and every one after it.
func fromLargeTx(t *testing.T, b realBinlog) string {
	t.Helper()

	all, err := gtid.Parse(b.gtids)
	require.NoError(t, err)
	before, err := gtid.Parse(b.gtidsBeforeLargeTx)
	require.NoError(t, err)

	return all.Subtract(before).String()
}

func TestTrimSourceLogsCarried(t *testing.T) {
	for _, b := range realBinlogs {
		t.Run(b.name+": a fetch that starts after a GTID event keeps its GTID", func(t *testing.T) {
			content := b.read(t)
			gtidEnd := eventEnd(content, b.largeTxAt)
			relayLogs := relayLogsOf(t, content[:gtidEnd])
			carried, err := receivedPart(relayLogs, relayLogs[0], uint64(gtidEnd))
			require.NoError(t, err)

			logs := stagedLogs(t, content[gtidEnd:])
			gtids, err := trimSourceLogs(logs, carried)

			require.NoError(t, err)
			assert.Equal(t, fromLargeTx(t, b), gtids)
			assert.Equal(t, []string{content[gtidEnd:b.stopAt]}, readAll(t, logs))
		})
	}

	t.Run("cuts the rest of a transaction the source never finished", func(t *testing.T) {
		b := realBinlogs[1]
		content := b.read(t)
		gtidEnd := eventEnd(content, b.largeTxAt)
		relayLogs := relayLogsOf(t, content[:gtidEnd])
		carried, err := receivedPart(relayLogs, relayLogs[0], uint64(gtidEnd))
		require.NoError(t, err)

		logs := stagedLogs(t, content[gtidEnd:b.largeTxAt+b.largeTxLen/2])
		gtids, err := trimSourceLogs(logs, carried)

		require.NoError(t, err)
		assert.Empty(t, gtids)
		assert.Equal(t, []string{""}, readAll(t, logs))
	})

	t.Run("refuses a log that does not continue the transaction", func(t *testing.T) {
		b := realBinlogs[1]
		content := b.read(t)
		gtidEnd := eventEnd(content, b.largeTxAt)
		relayLogs := relayLogsOf(t, content[:gtidEnd])
		carried, err := receivedPart(relayLogs, relayLogs[0], uint64(gtidEnd))
		require.NoError(t, err)

		logs := stagedLogs(t, content[b.largeTxAt:])
		_, err = trimSourceLogs(logs, carried)

		require.EqualError(t, err, fmt.Sprintf(
			"binlog.000004: the rest of transaction ee61b2b7-c64f-11f1-b6f6-a8a159366a35:5 should end at %d, but the next transaction starts at 0",
			b.largeTxLen-(gtidEnd-b.largeTxAt)))
	})

	t.Run("refuses an unfinished transaction in the first of several logs", func(t *testing.T) {
		b := realBinlogs[1]
		content := b.read(t)
		gtidEnd := eventEnd(content, b.largeTxAt)
		relayLogs := relayLogsOf(t, content[:gtidEnd])
		carried, err := receivedPart(relayLogs, relayLogs[0], uint64(gtidEnd))
		require.NoError(t, err)

		logs := stagedLogs(t, content[gtidEnd:b.largeTxAt+b.largeTxLen/2], binlogEvent("whole-five"))
		_, err = trimSourceLogs(logs, carried)

		require.ErrorIs(t, err, errTornBinlog)
		assert.ErrorContains(t, err, "binlog.000004 has")
	})
}
