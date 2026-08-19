package binlogsource

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/google/uuid"
	"github.com/pkg/errors"
)

// errPurged means the transactions a replica is missing are no longer in any log.
var errPurged = errors.New("required transactions are no longer in the binary logs")

// errStopParse ends a parse early once the caller has what it came for; parseBinlog
// swallows it.
var errStopParse = errors.New("stop parsing")

// parseBufferSize buffers the log: go-mysql reads straight from the file, two syscalls
// per event.
const parseBufferSize = 1 << 20

func readIndex(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.Wrapf(err, "open %s", path)
	}
	defer f.Close() //nolint:errcheck

	dir := filepath.Dir(path)
	var files []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		// Entries may be absolute, relative or "./name"; the files live next to the index.
		files = append(files, filepath.Join(dir, filepath.Base(line)))
	}
	if err := s.Err(); err != nil {
		return nil, errors.Wrapf(err, "scan %s", path)
	}
	if len(files) == 0 {
		return nil, errors.Errorf("%s lists no binary logs", path)
	}
	return files, nil
}

type onEvent func(e *replication.BinlogEvent, pos int64, body []byte) error

func parseBinlog(file string, raw bool, fn onEvent) error {
	f, err := os.Open(file)
	if err != nil {
		return errors.Wrapf(err, "open %s", file)
	}
	defer f.Close() //nolint:errcheck

	r := bufio.NewReaderSize(f, parseBufferSize)

	magic := make([]byte, len(replication.BinLogFileHeader))
	if _, err := io.ReadFull(r, magic); err != nil {
		return errors.Wrapf(err, "read header of %s", file)
	}
	if !bytes.Equal(magic, replication.BinLogFileHeader) {
		return errors.Errorf("%s is not a binary log", file)
	}

	p := replication.NewBinlogParser()
	p.SetRawMode(raw)
	// Rows events are the bulk of a binary log and nothing here reads one.
	p.SetRowsEventDecodeFunc(func(*replication.RowsEvent, []byte) error { return nil })

	pos := int64(binlogFileHeaderSize)
	// The first event (FormatDescriptionEvent) says whether the file's events
	// carry a trailer, itself included.
	crc := false

	walk := func(e *replication.BinlogEvent) error {
		pos += int64(e.Header.EventSize)

		if ev, ok := e.Event.(*replication.FormatDescriptionEvent); ok {
			crc = ev.ChecksumAlgorithm == replication.BINLOG_CHECKSUM_ALG_CRC32
		}
		if crc {
			if err := verifyChecksum(e); err != nil {
				return errors.Wrapf(err, "at %d in %s", pos, file)
			}
		}

		return fn(e, pos, eventBody(e, crc))
	}

	if err := p.ParseReader(r, walk); err != nil && errors.Cause(err) != errStopParse {
		return errors.Wrapf(err, "parse %s", file)
	}
	return nil
}

// previousGTIDs returns a log's PREVIOUS_GTIDS_EVENT: everything committed before that
// file was opened.
func previousGTIDs(file string) (*mysql.MysqlGTIDSet, error) {
	var found *mysql.MysqlGTIDSet

	err := parseBinlog(file, false, func(e *replication.BinlogEvent, _ int64, _ []byte) error {
		ev, ok := e.Event.(*replication.PreviousGTIDsEvent)
		if !ok {
			return nil
		}
		set, err := previousGTIDsOf(ev)
		if err != nil {
			return err
		}
		found = set
		return errStopParse
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, errors.Errorf("no PREVIOUS_GTIDS event in %s", file)
	}
	return found, nil
}

func previousGTIDsOf(ev *replication.PreviousGTIDsEvent) (*mysql.MysqlGTIDSet, error) {
	set, err := mysql.ParseMysqlGTIDSet(ev.GTIDSets)
	if err != nil {
		return nil, errors.Wrapf(err, "parse previous gtids %q", ev.GTIDSets)
	}
	return set.(*mysql.MysqlGTIDSet), nil
}

// remaining returns the newest binary log whose PREVIOUS_GTIDS the replica already has,
// and every file after it: everything the replica is missing and nothing before that.
func remaining(files []string, replicaSet mysql.GTIDSet) ([]string, error) {
	for i := len(files) - 1; i >= 0; i-- {
		prev, err := previousGTIDs(files[i])
		if err != nil {
			return nil, err
		}
		if replicaSet.Contain(prev) {
			return files[i:], nil
		}
	}
	return nil, errPurged
}

// transactions follows a binary log event by event and says where each transaction in
// it ends. Both the offset a source may serve up to and the set it advertises are drawn
// from that, so they have to read it from one place.
//
// What ends a transaction is an allowlist on purpose: mysqld writes SAVEPOINT and
// ROLLBACK TO SAVEPOINT into the middle of a row based transaction as plain query
// events, and taking either for the end would cut one in half. An unrecognised statement
// leaves the transaction open, which at worst stops short of what the log holds.
//
// An XA transaction of the client's is two here, a prepare and a commit, each with a
// GTID of its own and anything at all written between them. Both are whole on their
// own: a replica holding a prepared transaction it has no commit for is ordinary.
type transactions struct {
	state txnState
}

type txnState int

const (
	// Between transactions.
	txnIdle txnState = iota
	// A GTID event, but not yet the statement behind it -- which may be a transaction
	// of its own.
	txnPending
	// Inside BEGIN ... COMMIT.
	txnOpen
	// Inside XA START ... XA PREPARE.
	txnXA
)

func (t *transactions) gtid() {
	t.state = txnPending
}

// end records the event that closes a transaction: an XID event, or the XA prepare
// event that ends a prepared transaction and a one phase commit alike.
func (t *transactions) end() {
	t.state = txnIdle
}

// payload records a compressed transaction, which mysqld writes as a GTID event and one
// TRANSACTION_PAYLOAD_EVENT holding the BEGIN, the rows and the XID -- a whole committed
// transaction by construction. Only the GTID event stays outside, since the commit path
// writes it rather than the cache, so anything else in front of a payload event is a log
// no mysqld wrote.
func (t *transactions) payload() error {
	switch t.state {
	case txnPending:
		t.state = txnIdle
		return nil
	case txnIdle:
		return errors.New("compressed transaction with no GTID event in front of it")
	default:
		return errors.New("compressed transaction inside another transaction")
	}
}

func (t *transactions) query(query []byte) {
	stmt := statement(query)

	switch t.state {
	case txnOpen:
		if strings.EqualFold(stmt, "COMMIT") || strings.EqualFold(stmt, "ROLLBACK") {
			t.state = txnIdle
		}
		return
	case txnXA:
		// XA END only closes the branch: the prepare that ends the transaction is an
		// event of its own, as is a one phase commit -- read here too in case another
		// mysqld writes it as a statement.
		if onePhaseCommit(stmt) {
			t.state = txnIdle
		}
		return
	}

	if strings.EqualFold(stmt, "BEGIN") {
		t.state = txnOpen
		return
	}

	// Checked ahead of the statements that stand on their own, which is what XA START
	// looks like from here. XA BEGIN is the same statement under another name.
	if norm := normalize(stmt); strings.HasPrefix(norm, "XA START ") || strings.HasPrefix(norm, "XA BEGIN ") {
		t.state = txnXA
		return
	}

	// DDL, XA COMMIT and XA ROLLBACK of an already prepared transaction, and anything
	// else outside a transaction, is a transaction of its own.
	t.state = txnIdle
}

func (t *transactions) open() bool {
	return t.state != txnIdle
}

// statement trims a query event down to the bare statement, so the ones that end a
// transaction can be matched whole: a prefix match would take ROLLBACK TO SAVEPOINT `a`
// for a ROLLBACK.
func statement(query []byte) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(string(query)), ";"))
}

// onePhaseCommit reports whether a statement commits an XA transaction that was never
// prepared, which ends it rather than opening one of its own.
func onePhaseCommit(stmt string) bool {
	norm := normalize(stmt)
	return strings.HasPrefix(norm, "XA COMMIT ") && strings.HasSuffix(norm, " ONE PHASE")
}

// normalize upper cases a statement and puts single spaces between its words, so it
// can be matched with the surrounding space as a word boundary. Without it XA STARTED
// would pass for an XA START.
func normalize(stmt string) string {
	return strings.Join(strings.Fields(strings.ToUpper(stmt)), " ")
}

// binlogScan is what one pass over a binary log yields. The offset a source may serve
// that log to and the set it advertises have to agree, so both come out of one pass.
type binlogScan struct {
	file string

	// committed is the offset just past the last committed transaction. The old primary
	// may have died part way through writing one, and nothing will ever finish it.
	committed int64

	// complete is the offset just past the last whole event, which is where the scan
	// ended. Whatever lies between committed and it is the unfinished tail.
	complete int64

	// checksum says whether the file's events carry a CRC32 trailer. The events we
	// synthesise have to match it, or the replica reads four bytes too many or too few.
	checksum bool

	// gtids is the file's PREVIOUS_GTIDS plus every transaction committed in it, which
	// for the newest log is everything the source holds.
	gtids *mysql.MysqlGTIDSet
}

func scanBinlog(file string) (*binlogScan, error) {
	complete, err := completeLength(file)
	if err != nil {
		return nil, err
	}

	sc := &binlogScan{file: file, committed: binlogFileHeaderSize, complete: complete}

	var (
		txns       transactions
		pending    *gtid
		haveFormat bool
	)

	// PREVIOUS_GTIDS is the second event in a well formed log, so it has always been
	// seen by the time a transaction ends.
	commit := func() error {
		if pending == nil {
			return nil
		}
		defer func() { pending = nil }()
		if sc.gtids == nil {
			return errors.Errorf("transaction committed before any PREVIOUS_GTIDS event in %s", file)
		}
		sc.gtids.AddGTIDWithTag(pending.uuid, pending.tag, pending.gno)
		return nil
	}

	err = parseBinlog(file, true, func(e *replication.BinlogEvent, pos int64, body []byte) error {
		// The end of a transaction is this turning false below: one rule for every
		// event, rather than a verdict each of them carries.
		txnWasOpen := txns.open()

		switch e.Header.EventType {
		case replication.FORMAT_DESCRIPTION_EVENT:
			// Decoded even in raw mode: the parser needs it itself.
			ev, ok := e.Event.(*replication.FormatDescriptionEvent)
			if !ok {
				return errors.Errorf("format description event at %d did not decode", pos)
			}
			sc.checksum = ev.ChecksumAlgorithm == replication.BINLOG_CHECKSUM_ALG_CRC32
			haveFormat = true
		case replication.PREVIOUS_GTIDS_EVENT:
			ev := new(replication.PreviousGTIDsEvent)
			if err := ev.Decode(body); err != nil {
				return errors.Wrapf(err, "decode previous gtids event at %d", pos)
			}
			prev, err := previousGTIDsOf(ev)
			if err != nil {
				return err
			}
			sc.gtids = prev
		case replication.GTID_EVENT, replication.GTID_TAGGED_LOG_EVENT, replication.ANONYMOUS_GTID_EVENT:
			next, err := gtidNext(e.Header.EventType, body)
			if err != nil {
				return errors.Wrapf(err, "read gtid at %d in %s", pos, file)
			}
			pending = next
			txns.gtid()
		case replication.XID_EVENT, replication.XA_PREPARE_LOG_EVENT:
			// go-mysql has no type for the XA prepare event, and the flag that tells a
			// one phase commit from a prepare changes nothing here.
			txns.end()
		case replication.TRANSACTION_PAYLOAD_EVENT:
			// A payload event says all this needs to know by existing, so the blob is
			// never decompressed.
			if err := txns.payload(); err != nil {
				return errors.Wrapf(err, "at %d in %s", pos, file)
			}
		case replication.QUERY_EVENT:
			q := new(replication.QueryEvent)
			if err := q.Decode(body); err != nil {
				return errors.Wrapf(err, "decode query event at %d", pos)
			}
			txns.query(q.Query)
		}

		if !txns.open() {
			if txnWasOpen {
				if err := commit(); err != nil {
					return err
				}
			}
			sc.committed = pos
		}
		if pos >= complete {
			return errStopParse
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !haveFormat {
		return nil, errors.Errorf("no format description event in %s", file)
	}
	if sc.gtids == nil {
		return nil, errors.Errorf("no PREVIOUS_GTIDS event in %s", file)
	}

	return sc, nil
}

// eventBody is an event without its header and without its checksum trailer, which is
// what a go-mysql event type decodes.
func eventBody(e *replication.BinlogEvent, checksum bool) []byte {
	body := e.RawData[replication.EventHeaderSize:]
	if checksum {
		body = body[:len(body)-replication.BinlogChecksumLength]
	}
	return body
}

// gtid is one transaction's identifier, in the form a set takes it in. Kept apart
// rather than as a one element GTIDSet, which would cost two maps and a text round trip
// per transaction in the log.
type gtid struct {
	uuid uuid.UUID
	tag  mysql.Tag
	gno  int64
}

// gtidNext reads the GTID a GTID event assigns to the transaction behind it. 8.4
// writes a tagged event of its own type when the transaction carries a tag and the
// plain event otherwise.
func gtidNext(evType replication.EventType, body []byte) (*gtid, error) {
	// The tagged event embeds the plain one; only the encoding differs.
	ev := new(replication.GTIDEvent)
	if evType == replication.GTID_TAGGED_LOG_EVENT {
		tagged := new(replication.GtidTaggedLogEvent)
		if err := tagged.Decode(body); err != nil {
			return nil, err
		}
		ev = &tagged.GTIDEvent
	} else if err := ev.Decode(body); err != nil {
		return nil, err
	}

	u, err := uuid.FromBytes(ev.SID)
	if err != nil {
		return nil, err
	}
	return &gtid{uuid: u, tag: ev.Tag, gno: ev.GNO}, nil
}

// completeLength returns the offset just past the last whole event in a binary log.
// mysqld may be part way through writing the newest one, and a fragment handed to a
// replica ends the stream in an error it reports as a failure rather than as the end of
// the log. Binlog_sender reads only as far as mysqld's end position for the same reason.
func completeLength(file string) (int64, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, errors.Wrapf(err, "open %s", file)
	}
	defer f.Close() //nolint:errcheck

	// Walking the headers forwards reads every byte once, in order, so a buffered reader
	// serves the whole file in a handful of syscalls.
	r := bufio.NewReaderSize(f, parseBufferSize)
	if _, err := r.Discard(binlogFileHeaderSize); err != nil {
		return 0, errors.Wrapf(err, "read header of %s", file)
	}

	header := make([]byte, replication.EventHeaderSize)
	pos := int64(binlogFileHeaderSize)
	for {
		// A short read means the rest is the fragment mysqld is still writing, which
		// pos already excludes; anything else is a real failure.
		if _, err := io.ReadFull(r, header); err != nil {
			if isShortRead(err) {
				break
			}
			return 0, errors.Wrapf(err, "read event header at %d in %s", pos, file)
		}

		length := int64(binary.LittleEndian.Uint32(header[eventSizeOffset:]))
		if length < int64(replication.EventHeaderSize) {
			return 0, errors.Errorf("event at %d in %s claims to be %d bytes", pos, file, length)
		}
		if _, err := r.Discard(int(length) - replication.EventHeaderSize); err != nil {
			if isShortRead(err) {
				break
			}
			return 0, errors.Wrapf(err, "read event at %d in %s", pos, file)
		}
		pos += length
	}

	return pos, nil
}

func isShortRead(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
