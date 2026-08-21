package binlogsource

import (
	"bufio"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/google/uuid"
	"github.com/pkg/errors"
)

// ErrPurged means the transactions a replica is missing are no longer present
// in any available binary log. Waiting cannot fix this.
var ErrPurged = errors.New("required transactions are no longer in the binary logs")

// Index is the ordered list of binary log files a data directory holds.
type Index struct {
	Files []string
}

func ReadIndex(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.Wrapf(err, "open %s", path)
	}
	defer f.Close() //nolint:errcheck

	dir := filepath.Dir(path)
	idx := new(Index)
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		// Entries may be absolute, relative, or "./name"; the files we care
		// about always live next to the index.
		idx.Files = append(idx.Files, filepath.Join(dir, filepath.Base(line)))
	}
	if err := s.Err(); err != nil {
		return nil, errors.Wrapf(err, "scan %s", path)
	}
	if len(idx.Files) == 0 {
		return nil, errors.Errorf("%s lists no binary logs", path)
	}
	return idx, nil
}

// Since returns the named file and every file after it, which is the stretch a
// replica served from that file still needs.
func (i *Index) Since(file string) []string {
	for n, f := range i.Files {
		if f == file {
			return i.Files[n:]
		}
	}
	return nil
}

// PreviousGTIDs returns the PREVIOUS_GTIDS_EVENT set of a binary log file,
// which is everything committed before that file was opened.
func PreviousGTIDs(file string) (*mysql.MysqlGTIDSet, error) {
	var found *mysql.MysqlGTIDSet
	stop := errors.New("stop")

	p := replication.NewBinlogParser()
	err := p.ParseFile(file, 0, func(e *replication.BinlogEvent) error {
		ev, ok := e.Event.(*replication.PreviousGTIDsEvent)
		if !ok {
			return nil
		}
		set, err := mysql.ParseMysqlGTIDSet(ev.GTIDSets)
		if err != nil {
			return errors.Wrapf(err, "parse previous gtids %q", ev.GTIDSets)
		}
		found = set.(*mysql.MysqlGTIDSet)
		return stop
	})
	if err != nil && errors.Cause(err) != stop {
		return nil, errors.Wrapf(err, "parse %s", file)
	}
	if found == nil {
		return nil, errors.Errorf("no PREVIOUS_GTIDS event in %s", file)
	}
	return found, nil
}

// fileHasChecksum reports whether the events in a binary log carry a CRC32
// trailer. Events we synthesise ourselves have to match, or the replica reads
// four bytes too many or too few.
func fileHasChecksum(file string) (bool, error) {
	var alg replication.BinlogChecksum
	found := false
	stop := errors.New("stop")

	p := replication.NewBinlogParser()
	err := p.ParseFile(file, 0, func(e *replication.BinlogEvent) error {
		ev, ok := e.Event.(*replication.FormatDescriptionEvent)
		if !ok {
			return nil
		}
		alg, found = ev.ChecksumAlgorithm, true
		return stop
	})
	if err != nil && errors.Cause(err) != stop {
		return false, errors.Wrapf(err, "parse %s", file)
	}
	if !found {
		return false, errors.Errorf("no format description event in %s", file)
	}

	return alg == replication.BINLOG_CHECKSUM_ALG_CRC32, nil
}

// ExecutedGTIDs returns every transaction the indexed binary logs contain:
// the last file's PREVIOUS_GTIDS plus every complete transaction inside it.
func ExecutedGTIDs(idx *Index) (*mysql.MysqlGTIDSet, error) {
	last := idx.Files[len(idx.Files)-1]
	set, err := PreviousGTIDs(last)
	if err != nil {
		return nil, err
	}

	var pendingUUID uuid.UUID
	var pendingGNO int64
	havePending := false

	commit := func() {
		if havePending {
			set.AddGTID(pendingUUID, pendingGNO)
			havePending = false
		}
	}

	p := replication.NewBinlogParser()
	err = p.ParseFile(last, 0, func(e *replication.BinlogEvent) error {
		switch ev := e.Event.(type) {
		case *replication.GTIDEvent:
			u, err := uuid.FromBytes(ev.SID)
			if err != nil {
				return errors.Wrap(err, "decode gtid sid")
			}
			pendingUUID, pendingGNO, havePending = u, ev.GNO, true
		case *replication.XIDEvent:
			commit()
		case *replication.QueryEvent:
			if commitsTransaction(ev.Query) {
				commit()
			}
		}
		return nil
	})
	if err != nil {
		return nil, errors.Wrapf(err, "scan %s", last)
	}
	return set, nil
}

// StartFile returns the newest binary log whose PREVIOUS_GTIDS the replica
// already has. Streaming from there delivers everything the replica is missing
// and nothing that predates it.
func StartFile(idx *Index, replicaSet mysql.GTIDSet) (string, error) {
	for i := len(idx.Files) - 1; i >= 0; i-- {
		prev, err := PreviousGTIDs(idx.Files[i])
		if err != nil {
			return "", err
		}
		if replicaSet.Contain(prev) {
			return idx.Files[i], nil
		}
	}
	return "", ErrPurged
}

// commitsTransaction reports whether a query event ends the transaction it
// belongs to. Only BEGIN opens one; DDL and anything else stands alone.
func commitsTransaction(query []byte) bool {
	return !strings.EqualFold(strings.TrimSpace(string(query)), "BEGIN")
}

// committedLength returns the offset just past the last committed transaction in
// a binary log, along with whatever follows it outside a transaction.
//
// The old primary may have died part way through writing a transaction to its
// newest log. Nothing will ever finish it, and it is not in the set
// ExecutedGTIDs advertises either, so serving its events would only leave the
// replica holding a transaction it can never commit. Stopping at the last commit
// keeps the stream and the advertised set the same thing.
func committedLength(file string, checksum bool) (int64, error) {
	complete, err := completeLength(file)
	if err != nil {
		return 0, err
	}
	if complete <= binlogFileHeaderSize {
		return complete, nil
	}

	stop := errors.New("stop")
	safe := int64(binlogFileHeaderSize)
	open := false

	// Raw mode, so a rows event go-mysql cannot decode does not fail the dump.
	// Only query events have to be read, to tell BEGIN from a statement that is
	// its own transaction.
	p := replication.NewBinlogParser()
	p.SetRawMode(true)
	err = p.ParseFile(file, 0, func(e *replication.BinlogEvent) error {
		switch e.Header.EventType {
		case replication.GTID_EVENT, replication.GTID_TAGGED_LOG_EVENT, replication.ANONYMOUS_GTID_EVENT:
			open = true
		case replication.XID_EVENT:
			open = false
		case replication.QUERY_EVENT:
			body := e.RawData[replication.EventHeaderSize:]
			if checksum {
				body = body[:len(body)-replication.BinlogChecksumLength]
			}
			q := new(replication.QueryEvent)
			if err := q.Decode(body); err != nil {
				return errors.Wrapf(err, "decode query event at %d", e.Header.LogPos)
			}
			if commitsTransaction(q.Query) {
				open = false
			}
		}

		if !open {
			safe = int64(e.Header.LogPos)
		}
		if int64(e.Header.LogPos) >= complete {
			return stop
		}
		return nil
	})
	if err != nil && errors.Cause(err) != stop {
		return 0, errors.Wrapf(err, "scan %s", file)
	}

	return safe, nil
}

// completeLength returns the offset just past the last whole event in a binary
// log. mysqld may be part way through writing the newest one, and a fragment is
// not something a replica can be handed: go-mysql reports the short read as a
// plain message, so the stream would end in an error the replica reports as a
// failure rather than as the end of the log.
//
// Binlog_sender reads only as far as the end position mysqld reports to it, for
// the same reason.
func completeLength(file string) (int64, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, errors.Wrapf(err, "open %s", file)
	}
	defer f.Close() //nolint:errcheck

	info, err := f.Stat()
	if err != nil {
		return 0, errors.Wrapf(err, "stat %s", file)
	}
	size := info.Size()

	header := make([]byte, replication.EventHeaderSize)
	pos := int64(binlogFileHeaderSize)
	for pos+int64(len(header)) <= size {
		if _, err := f.ReadAt(header, pos); err != nil {
			return 0, errors.Wrapf(err, "read event header at %d in %s", pos, file)
		}

		length := int64(binary.LittleEndian.Uint32(header[eventSizeOffset:]))
		if length < int64(replication.EventHeaderSize) {
			return 0, errors.Errorf("event at %d in %s claims to be %d bytes", pos, file, length)
		}
		if pos+length > size {
			break
		}
		pos += length
	}

	return pos, nil
}
