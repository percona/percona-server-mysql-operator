package binlogsource

import (
	"bufio"
	"io"
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
			// DDL and other non-BEGIN statements terminate their transaction.
			if strings.ToUpper(strings.TrimSpace(string(ev.Query))) != "BEGIN" {
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

func isTruncated(err error) bool {
	cause := errors.Cause(err)
	return cause == io.EOF || cause == io.ErrUnexpectedEOF
}
