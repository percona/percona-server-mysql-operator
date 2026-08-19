package binlogsource

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testIndex(t *testing.T) []string {
	t.Helper()
	files, err := readIndex(filepath.Join("testdata", "binlog.index"))
	require.NoError(t, err)
	return files
}

func gtidString(g *gtid) string {
	set := mysql.MysqlGTIDSet{}
	set.AddGTIDWithTag(g.uuid, g.tag, g.gno)
	return set.String()
}

func TestReadIndexResolvesRelativePaths(t *testing.T) {
	files := testIndex(t)
	require.Len(t, files, 3)

	for _, f := range files {
		assert.FileExists(t, f)
	}
}

// The newest file's PREVIOUS_GTIDS is everything committed before it, so the set
// scanned out of that file must contain it.
func TestTheScannedSetCoversEveryFile(t *testing.T) {
	files := testIndex(t)
	last := files[len(files)-1]

	sc, err := scanBinlog(last)
	require.NoError(t, err)

	prev, err := previousGTIDs(last)
	require.NoError(t, err)

	assert.True(t, sc.gtids.Contain(prev))
	assert.NotEmpty(t, sc.gtids.String())
}

// A replica that has everything up to the last file must be served from the last file
// and nothing before it; one that has nothing gets every file.
func TestRemainingStartsAtTheNewestFullyCoveredFile(t *testing.T) {
	files := testIndex(t)

	last := files[len(files)-1]
	prev, err := previousGTIDs(last)
	require.NoError(t, err)

	got, err := remaining(files, prev)
	require.NoError(t, err)
	assert.Equal(t, []string{last}, got)

	// A replica that has nothing is served from the oldest file opened before any
	// transaction was committed.
	empty := mysql.NewMysqlGTIDSet()
	got, err = remaining(files, &empty)
	require.NoError(t, err)
	require.NotEmpty(t, got)
	assert.Equal(t, files[len(files)-len(got):], got, "the files served must be a suffix of the index")

	prev, err = previousGTIDs(got[0])
	require.NoError(t, err)
	assert.Empty(t, prev.String(), "the stream would start after transactions the replica does not have")
}

// A replica whose transactions predate every remaining log cannot be served at all:
// what it is missing has been purged.
func TestRemainingReportsPurgedLogs(t *testing.T) {
	files := testIndex(t)

	// An index of just the newest file is what is left once the older ones have been
	// purged, so its PREVIOUS_GTIDS is no longer reachable.
	purged := files[len(files)-1:]
	require.NotEmpty(t, mustPreviousGTIDs(t, purged[0]).String(),
		"the fixture's newest log must follow some transaction")

	set, err := mysql.ParseMysqlGTIDSet("11111111-1111-1111-1111-111111111111:1-5")
	require.NoError(t, err)

	_, err = remaining(purged, set)
	assert.ErrorIs(t, err, errPurged)
}

func mustPreviousGTIDs(t *testing.T, file string) *mysql.MysqlGTIDSet {
	t.Helper()
	set, err := previousGTIDs(file)
	require.NoError(t, err)
	return set
}

func TestChecksumIsReadFromTheFormatDescriptionEvent(t *testing.T) {
	sc, err := scanBinlog(filepath.Join("testdata", "binlog.000001"))
	require.NoError(t, err)
	assert.True(t, sc.checksum, "binary logs written by MySQL 8 carry a CRC32 trailer")
}

func TestScanOfAFileThatIsNotABinaryLog(t *testing.T) {
	_, err := scanBinlog(filepath.Join("testdata", "binlog.index"))
	require.Error(t, err)
}

// binlogFile writes a binary log into a directory of its own, so a test can rewrite a
// fixture without touching the fixture itself.
func binlogFile(t *testing.T, name string, raw []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, raw, 0o644))

	return path
}

func truncatedCopy(t *testing.T, file string, n int) string {
	t.Helper()

	raw, err := os.ReadFile(file)
	require.NoError(t, err)

	return binlogFile(t, filepath.Base(file), raw[:len(raw)-n])
}

// wrappedCopy returns a copy of a binary log carrying the log_pos values mysqld would
// have written had its events landed either side of the 4GiB mark, where the four byte
// field overflows. It stands in for a log over 4GiB without putting one on disk: every
// event is whole and its length is the truth, and only the positions lie.
func wrappedCopy(t *testing.T, file string) string {
	t.Helper()

	raw, err := os.ReadFile(file)
	require.NoError(t, err)

	// Puts the wrap half way in, so the copy carries positions from either side of it.
	shift := int64(1)<<32 - int64(len(raw))/2

	for pos := int64(binlogFileHeaderSize); pos < int64(len(raw)); {
		event := raw[pos:]
		size := int64(binary.LittleEndian.Uint32(event[eventSizeOffset:]))
		require.True(t, size >= int64(replication.EventHeaderSize) && size <= int64(len(event)),
			"%s is not a chain of whole events", file)
		event = event[:size]
		pos += size

		// Dropping the high bytes is the wrap itself. The trailer covers the header the
		// position sits in, so it has to be recomputed with it.
		binary.LittleEndian.PutUint32(event[eventLogPosOffset:], uint32(shift+pos))
		setChecksum(event)
	}

	return binlogFile(t, filepath.Base(file), raw)
}

// damagedCopy flips the byte at off and leaves every checksum trailer as it was, which
// is what damage to a log on disk looks like.
func damagedCopy(t *testing.T, file string, off int64) string {
	t.Helper()

	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	raw[off] ^= 0xff

	return binlogFile(t, filepath.Base(file), raw)
}

func secondEvent(t *testing.T, file string) int64 {
	t.Helper()

	raw, err := os.ReadFile(file)
	require.NoError(t, err)

	return int64(binlogFileHeaderSize) + int64(binary.LittleEndian.Uint32(raw[binlogFileHeaderSize+eventSizeOffset:]))
}

// The checksum covers an event's header as well as its body, so it is the one thing
// standing between a damaged header and a source that trusts it. Damage to log_pos in
// particular has nothing else to catch it: no reader here reads that field.
func TestADamagedEventHeaderIsRejected(t *testing.T) {
	file := filepath.Join("testdata", "binlog.000003")

	_, err := scanBinlog(damagedCopy(t, file, secondEvent(t, file)+eventLogPosOffset))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum")
}

// mysqld rewrites LOG_EVENT_BINLOG_IN_USE_F without touching the trailer, so the
// checksum only ever matches the event with the flag clear. The log a failover source
// cares about most is the one the old primary died writing, always marked in use.
func TestAnInUseLogPassesChecksumVerification(t *testing.T) {
	for _, name := range []string{"savepoint.000001", "compress.000001", "xa.000002"} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join("testdata", name)

			raw, err := os.ReadFile(file)
			require.NoError(t, err)
			flags := binary.LittleEndian.Uint16(raw[binlogFileHeaderSize+eventFlagsOffset:])
			require.NotZero(t, flags&replication.LOG_EVENT_BINLOG_IN_USE_F, "the fixture is not marked in use")

			_, err = scanBinlog(file)
			require.NoError(t, err)
		})
	}
}

// Dropping the in use flag before checking the format description event is not the
// same as not checking it: damage anywhere else in that event still has to be caught.
func TestADamagedFormatDescriptionEventIsRejected(t *testing.T) {
	file := filepath.Join("testdata", "binlog.000003")

	_, err := scanBinlog(damagedCopy(t, file, binlogFileHeaderSize))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum")
}

func TestCompleteLengthOfAClosedLogIsItsSize(t *testing.T) {
	file := filepath.Join("testdata", "binlog.000003")

	info, err := os.Stat(file)
	require.NoError(t, err)

	n, err := completeLength(file)
	require.NoError(t, err)
	assert.Equal(t, info.Size(), n, "every event in a closed log is whole")
}

// However much of the last event is missing, the answer is where that event begins:
// everything before it is still whole.
func TestCompleteLengthStopsWhereTheFragmentBegins(t *testing.T) {
	file := filepath.Join("testdata", "binlog.000003")

	size, err := completeLength(file)
	require.NoError(t, err)

	// One byte short is enough to make the last event a fragment.
	begins, err := completeLength(truncatedCopy(t, file, 1))
	require.NoError(t, err)
	assert.Less(t, begins, size)

	for _, missing := range []int{4, 8, replication.EventHeaderSize + 1} {
		n, err := completeLength(truncatedCopy(t, file, missing))
		require.NoError(t, err)
		assert.Equal(t, begins, n, "%d bytes short", missing)
	}
}

func TestCompleteLengthRejectsAnImpossibleEventSize(t *testing.T) {
	raw := make([]byte, binlogFileHeaderSize+replication.EventHeaderSize)
	copy(raw, replication.BinLogFileHeader)
	binary.LittleEndian.PutUint32(raw[binlogFileHeaderSize+eventSizeOffset:], 5)

	file := filepath.Join(t.TempDir(), "binlog.000001")
	require.NoError(t, os.WriteFile(file, raw, 0o644))

	_, err := completeLength(file)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claims to be 5 bytes")
}

// uncommittedCopy returns a copy of a binary log whose final transaction has lost its
// commit, as the old primary's newest log looks if it died part way through one.
func uncommittedCopy(t *testing.T, file string) (path string, lastCommit int64) {
	t.Helper()

	p := replication.NewBinlogParser()
	p.SetRowsEventDecodeFunc(func(*replication.RowsEvent, []byte) error { return nil })
	require.NoError(t, p.ParseFile(file, 0, func(e *replication.BinlogEvent) error {
		if _, ok := e.Event.(*replication.XIDEvent); ok {
			lastCommit = int64(e.Header.LogPos) - int64(e.Header.EventSize)
		}
		return nil
	}))
	require.Positive(t, lastCommit, "%s has no committed transaction to cut", file)

	raw, err := os.ReadFile(file)
	require.NoError(t, err)

	return binlogFile(t, filepath.Base(file), raw[:lastCommit]), lastCommit
}

// Everything in a closed log is committed, including the rotate event that ends it --
// that one is not inside a transaction and must still be served.
func TestCommittedLengthOfAClosedLogIsItsSize(t *testing.T) {
	for _, name := range []string{"binlog.000002", "binlog.000003"} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join("testdata", name)

			info, err := os.Stat(file)
			require.NoError(t, err)

			sc, err := scanBinlog(file)
			require.NoError(t, err)
			assert.Equal(t, info.Size(), sc.committed)
			assert.True(t, sc.checksum)
		})
	}
}

// The scan runs in raw mode, decoding each GTID event from its own bytes rather than
// letting the parser do it. What comes out has to be what the parser would have
// produced, or the source advertises transactions the logs do not hold.
func TestTheRawGTIDDecodeAgreesWithTheParser(t *testing.T) {
	for _, name := range []string{
		"binlog.000002", "binlog.000003", "savepoint.000001",
		"xa.000001", "xa.000002", "compress.000001",
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join("testdata", name)

			var want []string
			require.NoError(t, parseBinlog(file, false, func(e *replication.BinlogEvent, _ int64, _ []byte) error {
				ev, ok := e.Event.(mysql.BinlogGTIDEvent)
				if !ok {
					return nil
				}
				next, err := ev.GTIDNext()
				require.NoError(t, err)
				want = append(want, next.String())
				return nil
			}))
			require.NotEmpty(t, want, "the fixture holds no GTID event")

			var got []string
			require.NoError(t, parseBinlog(file, true, func(e *replication.BinlogEvent, _ int64, body []byte) error {
				switch e.Header.EventType {
				case replication.GTID_EVENT, replication.GTID_TAGGED_LOG_EVENT, replication.ANONYMOUS_GTID_EVENT:
					next, err := gtidNext(e.Header.EventType, body)
					require.NoError(t, err)
					got = append(got, gtidString(next))
				}
				return nil
			}))

			assert.Equal(t, want, got)
		})
	}
}

// 8.4 writes a transaction whose GTID carries a tag as an event of its own type. No
// fixture here holds one, so the body is the one go-mysql tests its own decoder
// against, wrapped in an event as it would sit in a log.
func TestGTIDNextReadsATaggedGTIDEvent(t *testing.T) {
	// 896e7882-18fe-11ef-ab88-22222d34d411:foobaz:1
	body := []byte{
		0x2, 0x76, 0x0, 0x0, 0x2, 0x2, 0x25, 0x2, 0xdc, 0xf0, 0x9, 0x2, 0x30, 0xf9, 0x3, 0x22, 0xbd, 0x3,
		0xad, 0x2, 0x21, 0x2, 0x44, 0x44, 0x5a, 0x68, 0x51, 0x3, 0x22, 0x4, 0x4, 0x6, 0xc, 0x66, 0x6f, 0x6f, 0x62,
		0x61, 0x7a, 0x8, 0x0, 0xa, 0x4, 0xc, 0x7f, 0x15, 0x83, 0x22, 0x2d, 0x5c, 0x2e, 0x6, 0x10, 0x49, 0x3, 0x12,
		0xc3, 0x2, 0xb,
	}

	// The trailer is what the body has to be cut back from, so read the same event
	// both with one and without.
	for name, checksum := range map[string]bool{"with a trailer": true, "without one": false} {
		t.Run(name, func(t *testing.T) {
			raw := append(make([]byte, replication.EventHeaderSize), body...)
			if checksum {
				raw = binary.LittleEndian.AppendUint32(raw, crc32.ChecksumIEEE(raw))
			}

			ev := &replication.BinlogEvent{
				Header:  &replication.EventHeader{EventType: replication.GTID_TAGGED_LOG_EVENT},
				RawData: raw,
			}
			next, err := gtidNext(ev.Header.EventType, eventBody(ev, checksum))
			require.NoError(t, err)
			assert.Equal(t, "896e7882-18fe-11ef-ab88-22222d34d411:foobaz:1", gtidString(next))
		})
	}
}

// The log the old primary died writing ends in the fragment of the event it was part
// way through, and the set the source advertises is read from that same log. A scan
// that failed on the fragment would leave the source unable to answer for its logs.
func TestScanSurvivesAFragmentedTail(t *testing.T) {
	file := filepath.Join("testdata", "binlog.000003")

	whole, err := scanBinlog(file)
	require.NoError(t, err)

	for _, missing := range []int{1, 4, replication.EventHeaderSize + 1} {
		t.Run(fmt.Sprintf("%d bytes short", missing), func(t *testing.T) {
			sc, err := scanBinlog(truncatedCopy(t, file, missing))
			require.NoError(t, err)

			// Nothing is lost but the rotate event that closes the log, which is no
			// transaction and carries no GTID.
			assert.Equal(t, whole.gtids.String(), sc.gtids.String())
			assert.Positive(t, sc.committed)
			assert.Less(t, sc.committed, whole.committed)
		})
	}
}

// A transaction nothing will ever commit is left out entirely, not served up to its
// last whole event.
func TestCommittedLengthDropsAnUncommittedTail(t *testing.T) {
	file, lastCommit := uncommittedCopy(t, filepath.Join("testdata", "binlog.000003"))

	whole, err := completeLength(file)
	require.NoError(t, err)
	require.Equal(t, lastCommit, whole, "the cut should land on an event boundary")

	sc, err := scanBinlog(file)
	require.NoError(t, err)
	assert.Less(t, sc.committed, whole, "the dangling transaction is still being served")
	assert.Equal(t, whole, sc.complete, "the scan read the log to its last whole event")

	// Everything up to the GTID event that opened it, and nothing after.
	assert.Equal(t, int64(198), sc.committed)
}

// A binary log over 4GiB carries log_pos values that have wrapped, so how far it may
// be served is accumulated from the event lengths instead. Read from the headers, the
// answer comes from the wrong side of the wrap.
func TestCommittedLengthIgnoresWrappedLogPos(t *testing.T) {
	uncommitted, _ := uncommittedCopy(t, filepath.Join("testdata", "binlog.000003"))

	for name, file := range map[string]string{
		"closed log":          filepath.Join("testdata", "binlog.000002"),
		"cut mid transaction": uncommitted,
	} {
		t.Run(name, func(t *testing.T) {
			want, err := scanBinlog(file)
			require.NoError(t, err)
			require.Positive(t, want.committed)

			got, err := scanBinlog(wrappedCopy(t, file))
			require.NoError(t, err)
			assert.Equal(t, want.committed, got.committed, "the wrapped positions were taken for the truth")
		})
	}
}

// truncation is one event boundary of a fixture: how far a source cut off there may
// serve, and what it has to advertise as executed.
type truncation struct {
	end   int64
	safe  int64
	gtids string
}

// savepointTruncations works out what every event boundary of
// testdata/savepoint.000001 demands. Its last transaction is one mysqld wrote for
//
//	BEGIN; INSERT; SAVEPOINT a; INSERT; SAVEPOINT b; INSERT;
//	ROLLBACK TO SAVEPOINT b; RELEASE SAVEPOINT a; INSERT; COMMIT
//
// which under row based logging comes out as rows events with the two SAVEPOINT
// statements between them as plain query events. Nothing in the fixture sits outside a
// transaction, so a GTID event opens each one and an XID event commits it.
func savepointTruncations(t *testing.T) (file string, cuts []truncation) {
	t.Helper()

	file = filepath.Join("testdata", "savepoint.000001")

	var (
		set     *mysql.MysqlGTIDSet
		pending mysql.GTIDSet
		safe    = int64(binlogFileHeaderSize)
		open    bool
	)

	p := replication.NewBinlogParser()
	p.SetRowsEventDecodeFunc(func(*replication.RowsEvent, []byte) error { return nil })
	require.NoError(t, p.ParseFile(file, 0, func(e *replication.BinlogEvent) error {
		switch ev := e.Event.(type) {
		case *replication.PreviousGTIDsEvent:
			prev, err := mysql.ParseMysqlGTIDSet(ev.GTIDSets)
			require.NoError(t, err)
			set = prev.(*mysql.MysqlGTIDSet)
		case mysql.BinlogGTIDEvent:
			next, err := ev.GTIDNext()
			require.NoError(t, err)
			pending, open = next, true
		case *replication.XIDEvent:
			require.NoError(t, set.Update(pending.String()))
			open = false
		}

		end := int64(e.Header.LogPos)
		if !open {
			safe = end
		}
		// Before PREVIOUS_GTIDS there is no log a source could serve at all.
		if set != nil {
			cuts = append(cuts, truncation{end: end, safe: safe, gtids: set.String()})
		}
		return nil
	}))

	require.NotEmpty(t, cuts)
	return file, cuts
}

// A savepoint is a query event in the middle of a transaction. Wherever the old
// primary died inside one, none of it may be served and none of it advertised, or the
// replica is left holding a transaction it can never commit.
func TestCommittedLengthSpansASavepoint(t *testing.T) {
	file, cuts := savepointTruncations(t)

	size, err := completeLength(file)
	require.NoError(t, err)

	for _, c := range cuts {
		t.Run(fmt.Sprintf("cut at %d", c.end), func(t *testing.T) {
			cut := truncatedCopy(t, file, int(size-c.end))

			sc, err := scanBinlog(cut)
			require.NoError(t, err)
			assert.Equal(t, c.safe, sc.committed, "served past the last commit")
			assert.Equal(t, c.gtids, sc.gtids.String(), "advertised a transaction the stream does not carry")
		})
	}
}

// A transaction ends at COMMIT or ROLLBACK and at nothing else. ROLLBACK TO SAVEPOINT
// is the one that looks most like an end and is not.
func TestTransactionsEndOnlyAtACommitOrARollback(t *testing.T) {
	for name, tc := range map[string]struct {
		queries []string
		open    bool
	}{
		"savepoint":                              {[]string{"BEGIN", "SAVEPOINT `a`"}, true},
		"rollback to savepoint":                  {[]string{"BEGIN", "SAVEPOINT `a`", "ROLLBACK TO SAVEPOINT `a`"}, true},
		"rollback to a savepoint named rollback": {[]string{"BEGIN", "ROLLBACK TO `rollback`"}, true},
		"a statement of its own":                 {[]string{"CREATE TABLE app.t (id INT)"}, false},
		"a statement inside one":                 {[]string{"BEGIN", "DELETE FROM app.t"}, true},
		"commit":                                 {[]string{"BEGIN", "COMMIT"}, false},
		"rollback":                               {[]string{"BEGIN", "ROLLBACK"}, false},
		"rollback with a semicolon":              {[]string{"BEGIN", " rollback; "}, false},
	} {
		t.Run(name, func(t *testing.T) {
			var txns transactions
			txns.gtid()

			for _, q := range tc.queries {
				txns.query([]byte(q))
			}

			assert.Equal(t, tc.open, txns.open())
		})
	}
}

// txnUnit is one transaction of a fixture: the offset its GTID event begins at, and
// the offset the event that ends it stops at.
type txnUnit struct{ start, end int64 }

// xaUnits says where every transaction of the XA fixtures begins and ends. The offsets
// were read off the files when mysqld 8.4 wrote them rather than worked out here: a
// test that decided for itself where a transaction ends would only be asking the code
// under test its own question.
//
// The session that produced them did, in order:
//
//	XA START 'x1'; INSERT; XA END 'x1'; XA PREPARE 'x1'; XA COMMIT 'x1'
//	XA START 'x2'; INSERT; XA END 'x2'; XA COMMIT 'x2' ONE PHASE
//	XA START 'x3'; INSERT; XA END 'x3'; XA PREPARE 'x3'
//	FLUSH BINARY LOGS
//	XA COMMIT 'x3'
//	XA START 'x4'; INSERT; XA END 'x4'; XA PREPARE 'x4'
//	BEGIN; INSERT; COMMIT
//	XA COMMIT 'x4'
//	XA START 'x5'; INSERT; XA END 'x5'; XA PREPARE 'x5'
//
// A prepare and the commit that follows it are two transactions with a GTID each, so
// they can be split across files and have other transactions between them; both happen
// here. The second file ends on a prepared transaction nothing has committed, which is
// whole and belongs in the advertised set all the same.
var xaUnits = map[string][]txnUnit{
	"xa.000001": {
		{198, 575},   // XA START x1, rows, XA END, XA_PREPARE
		{575, 741},   // XA COMMIT x1
		{741, 1118},  // XA START x2, rows, XA END, XA_PREPARE one phase
		{1118, 1495}, // XA START x3, rows, XA END, XA_PREPARE
	},
	"xa.000002": {
		{198, 364},   // XA COMMIT x3, prepared in the file before
		{364, 741},   // XA START x4, rows, XA END, XA_PREPARE
		{741, 1008},  // BEGIN, rows, XID, between the prepare of x4 and its commit
		{1008, 1174}, // XA COMMIT x4
		{1174, 1551}, // XA START x5, rows, XA END, XA_PREPARE, never committed
	},
}

// unitTruncations works out what every event boundary of a fixture demands, from the
// transactions units says it holds.
func unitTruncations(t *testing.T, name string, units []txnUnit) (file string, cuts []truncation) {
	t.Helper()

	file = filepath.Join("testdata", name)
	require.NotEmpty(t, units)

	var (
		prev    *mysql.MysqlGTIDSet
		prevEnd int64
		gtids   []string
		ends    []int64
	)
	p := replication.NewBinlogParser()
	p.SetRowsEventDecodeFunc(func(*replication.RowsEvent, []byte) error { return nil })
	require.NoError(t, p.ParseFile(file, 0, func(e *replication.BinlogEvent) error {
		switch ev := e.Event.(type) {
		case *replication.PreviousGTIDsEvent:
			set, err := mysql.ParseMysqlGTIDSet(ev.GTIDSets)
			require.NoError(t, err)
			prev, prevEnd = set.(*mysql.MysqlGTIDSet), int64(e.Header.LogPos)
		case mysql.BinlogGTIDEvent:
			next, err := ev.GTIDNext()
			require.NoError(t, err)
			gtids = append(gtids, next.String())
		}
		ends = append(ends, int64(e.Header.LogPos))
		return nil
	}))
	require.Len(t, gtids, len(units), "the fixture holds a transaction the unit table does not name")

	inside := func(pos int64) bool {
		for _, u := range units {
			if u.start < pos && pos < u.end {
				return true
			}
		}
		return false
	}

	safe := int64(binlogFileHeaderSize)
	for _, end := range ends {
		if !inside(end) {
			safe = end
		}
		// Before PREVIOUS_GTIDS there is no log a source could serve at all.
		if end < prevEnd {
			continue
		}
		want := prev.Clone().(*mysql.MysqlGTIDSet)
		for i, u := range units {
			if u.end <= end {
				require.NoError(t, want.Update(gtids[i]))
			}
		}
		cuts = append(cuts, truncation{end: end, safe: safe, gtids: want.String()})
	}

	require.NotEmpty(t, cuts)
	return file, cuts
}

// XA splits one transaction of the client's into two of mysqld's, each with a GTID of
// its own. The invariant is per transaction as mysqld wrote them: wherever the old
// primary died, a source may serve no half of one and advertise no GTID whose
// transaction is not on disk in full.
func TestCommittedLengthSpansAnXATransaction(t *testing.T) {
	for name := range xaUnits {
		t.Run(name, func(t *testing.T) {
			file, cuts := unitTruncations(t, name, xaUnits[name])

			size, err := completeLength(file)
			require.NoError(t, err)

			for _, c := range cuts {
				t.Run(fmt.Sprintf("cut at %d", c.end), func(t *testing.T) {
					cut := truncatedCopy(t, file, int(size-c.end))

					sc, err := scanBinlog(cut)
					require.NoError(t, err)
					assert.Equal(t, c.safe, sc.committed, "served past the last commit")
					assert.Equal(t, c.gtids, sc.gtids.String(), "advertised a transaction the stream does not carry")
				})
			}
		})
	}
}

// step drives the transaction machine one event on. Whether that event ended a
// transaction is read off the state the steps leave behind, as the scan reads it.
type step func(*transactions)

func gtidStep(t *transactions) { t.gtid() }

// endStep is the XID event that commits a plain transaction and the XA prepare event
// that ends a prepared one alike.
func endStep(t *transactions) { t.end() }

func payloadStep(t *transactions) { _ = t.payload() }

func queryStep(q string) step {
	return func(t *transactions) { t.query([]byte(q)) }
}

// An XA transaction is opened and closed by query events too, but not the ones that
// open and close a plain one. XA START reads like a statement standing on its own, and
// taking it for one cuts every XA transaction in half at its first event.
func TestTransactionsEndAtAnXATerminator(t *testing.T) {
	const (
		start = "XA START X'7831',X'',1"
		end   = "XA END X'7831',X'',1"
	)

	for name, tc := range map[string]struct {
		steps []step
		open  bool
	}{
		"xa start opens one":                      {[]step{gtidStep, queryStep(start)}, true},
		"xa begin is xa start under another name": {[]step{gtidStep, queryStep("XA BEGIN X'7831',X'',1")}, true},
		"xa end leaves it open":                   {[]step{gtidStep, queryStep(start), queryStep(end)}, true},
		"anything else leaves it open":            {[]step{gtidStep, queryStep(start), queryStep("SAVEPOINT `a`")}, true},
		"xa prepare ends it":                      {[]step{gtidStep, queryStep(start), queryStep(end), endStep}, false},
		"a one phase commit ends it":              {[]step{gtidStep, queryStep(start), queryStep(end), queryStep("XA COMMIT X'7831',X'',1 ONE PHASE")}, false},
		"a detached commit stands alone":          {[]step{gtidStep, queryStep("XA COMMIT X'7831',X'',1")}, false},
		"a detached rollback stands alone":        {[]step{gtidStep, queryStep("XA ROLLBACK X'7831',X'',1")}, false},
		"a plain transaction after a prepare":     {[]step{gtidStep, queryStep(start), queryStep(end), endStep, gtidStep, queryStep("BEGIN")}, true},
		"a plain transaction still ends":          {[]step{gtidStep, queryStep("BEGIN"), endStep}, false},
	} {
		t.Run(name, func(t *testing.T) {
			var txns transactions

			for _, s := range tc.steps {
				s(&txns)
			}

			assert.Equal(t, tc.open, txns.open())
		})
	}
}

// compressUnits says where every transaction of testdata/compress.000001 begins and
// ends, read off the file when mysqld 8.4 wrote it rather than worked out here.
//
// The session that produced it did, in order, with binlog_transaction_compression
// toggled as noted:
//
//	ON:  BEGIN; INSERT; COMMIT
//	ON:  INSERT                                     -- its own transaction
//	OFF: BEGIN; INSERT; COMMIT
//	ON:  BEGIN; INSERT; INSERT; COMMIT
//	ON:  CREATE TABLE app.d (id INT)
//	ON:  BEGIN; INSERT; SAVEPOINT a; INSERT; ROLLBACK TO SAVEPOINT a; INSERT; COMMIT
//	ON:  XA START 'x1'; INSERT; XA END 'x1'; XA PREPARE 'x1'
//	ON:  XA COMMIT 'x1'
//	ON:  XA START 'x2'; INSERT; XA END 'x2'; XA COMMIT 'x2' ONE PHASE
//	ON:  XA START 'x3'; INSERT; XA END 'x3'; XA PREPARE 'x3'
//
// A compressed transaction is two events at the top level and no more: the GTID event,
// written by the commit path rather than cached, and one payload event holding the
// BEGIN, the rows and the XID. The setting is per session and read at commit, so
// compressed and uncompressed transactions interleave freely; what bypasses the
// transaction cache -- the DDL, the detached XA COMMIT -- is never wrapped at all.
var compressUnits = []txnUnit{
	{198, 445},   // GTID, payload
	{445, 693},   // GTID, payload
	{693, 1169},  // GTID, BEGIN, table map, rows, XID -- uncompressed
	{1169, 1425}, // GTID, payload holding two rows events
	{1425, 1612}, // GTID, CREATE TABLE -- DDL is never wrapped
	{1612, 1890}, // GTID, payload holding the savepoints
	{1890, 2168}, // GTID, payload holding XA START through XA PREPARE
	{2168, 2334}, // GTID, XA COMMIT -- detached, so never wrapped
	{2334, 2612}, // GTID, payload holding a one phase commit
	{2612, 2891}, // GTID, payload holding a prepare nothing commits
}

// A compressed transaction ends at the payload event and nothing else, since nothing
// else of it is visible. Anything but a GTID event in front of one is a log no mysqld
// wrote.
func TestTransactionsEndAtACompressedPayload(t *testing.T) {
	for name, tc := range map[string]struct {
		steps   []step
		wantErr bool
	}{
		"a gtid event ends at one":         {[]step{gtidStep}, false},
		"outside a transaction":            {nil, true},
		"after a payload already ended it": {[]step{gtidStep, payloadStep}, true},
		"inside a plain transaction":       {[]step{gtidStep, queryStep("BEGIN")}, true},
		"inside an xa transaction":         {[]step{gtidStep, queryStep("XA START X'7831',X'',1")}, true},
	} {
		t.Run(name, func(t *testing.T) {
			var txns transactions
			for _, s := range tc.steps {
				s(&txns)
			}

			err := txns.payload()
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.False(t, txns.open(), "the transaction the payload committed is still open")
		})
	}
}

// Every transaction in the fixture is committed, including the prepared one it ends
// on, so a source may serve all of it.
func TestCommittedLengthOfACompressedLog(t *testing.T) {
	file := filepath.Join("testdata", "compress.000001")

	info, err := os.Stat(file)
	require.NoError(t, err)

	sc, err := scanBinlog(file)
	require.NoError(t, err)
	assert.Equal(t, info.Size(), sc.committed)
	assert.True(t, sc.checksum)
}

// A compressed transaction is one event, so wherever the old primary died it is either
// wholly on disk or not there at all. The case this turns on is a GTID event whose
// payload did not land, exactly as for a GTID with no XID after it.
func TestCommittedLengthSpansACompressedTransaction(t *testing.T) {
	file, cuts := unitTruncations(t, "compress.000001", compressUnits)

	size, err := completeLength(file)
	require.NoError(t, err)

	for _, c := range cuts {
		t.Run(fmt.Sprintf("cut at %d", c.end), func(t *testing.T) {
			cut := truncatedCopy(t, file, int(size-c.end))

			sc, err := scanBinlog(cut)
			require.NoError(t, err)
			assert.Equal(t, c.safe, sc.committed, "served past the last commit")
			assert.Equal(t, c.gtids, sc.gtids.String(), "advertised a transaction the stream does not carry")
		})
	}
}

func topLevelTypes(t *testing.T, file string, raw bool) (types []replication.EventType, payloads []*replication.BinlogEvent) {
	t.Helper()

	require.NoError(t, parseBinlog(file, raw, func(e *replication.BinlogEvent, _ int64, _ []byte) error {
		types = append(types, e.Header.EventType)
		if e.Header.EventType == replication.TRANSACTION_PAYLOAD_EVENT {
			payloads = append(payloads, e)
		}
		return nil
	}))

	return types, payloads
}

// Two things have to hold of the go-mysql the module pins, and both are worth failing
// on here rather than during a failover. A payload event reaches the caller as one
// opaque event, whichever mode the parse runs in -- were the nested events replayed,
// the inner XID would arrive too and a transaction would be counted twice. And the
// commit really is inside the blob, which is what makes the payload a commit marker.
func TestACompressedPayloadIsOneOpaqueCommittedTransaction(t *testing.T) {
	file := filepath.Join("testdata", "compress.000001")

	rawTypes, rawPayloads := topLevelTypes(t, file, true)
	decodedTypes, decodedPayloads := topLevelTypes(t, file, false)

	assert.Equal(t, rawTypes, decodedTypes, "the events a reader sees depend on the parse mode")
	require.Len(t, rawPayloads, 7, "the fixture should hold seven compressed transactions")
	require.Len(t, decodedPayloads, len(rawPayloads))

	// One event per transaction and no more: a payload event never sits next to
	// another, and only a GTID event comes before one.
	for i, ty := range rawTypes {
		if ty != replication.TRANSACTION_PAYLOAD_EVENT {
			continue
		}
		require.Positive(t, i)
		assert.Contains(t,
			[]replication.EventType{
				replication.GTID_EVENT,
				replication.GTID_TAGGED_LOG_EVENT,
				replication.ANONYMOUS_GTID_EVENT,
			},
			rawTypes[i-1], "a payload event that no GTID event opens")
	}

	// Raw mode never touches the blob, which is why a compressed log can be framed
	// without decompressing a byte of it.
	for _, e := range rawPayloads {
		_, decoded := e.Event.(*replication.TransactionPayloadEvent)
		assert.False(t, decoded, "raw mode decompressed a payload it did not have to")
	}

	// And what the blob holds is a whole transaction, ending in the commit that never
	// appears at the top level.
	for _, e := range decodedPayloads {
		ev, ok := e.Event.(*replication.TransactionPayloadEvent)
		require.True(t, ok, "payload event at %d did not decode", e.Header.LogPos)
		require.NotEmpty(t, ev.Events)

		last := ev.Events[len(ev.Events)-1].Header.EventType
		assert.Contains(t,
			[]replication.EventType{replication.XID_EVENT, replication.XA_PREPARE_LOG_EVENT},
			last, "the payload at %d does not end in a commit", e.Header.LogPos)
	}
}
