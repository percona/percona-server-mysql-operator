package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/percona/percona-server-mysql-operator/pkg/mysql/gtid"
)

var errTornBinlog = errors.New("binary log ends inside an event")

// trimSourceLogs cuts every staged log back to the end of its last complete
// transaction and returns the GTIDs of the transactions left in them
func trimSourceLogs(sourceLogs []string, carried *carriedTx) (string, error) {
	scans := make([]logScan, 0, len(sourceLogs))
	gtids := make(gtid.Set)

	for i, path := range sourceLogs {
		// The first log is cut at the position the replica had already read, which
		// is an event boundary. Every later one is a whole file behind a magic number.
		var start int64
		first := carried
		if i > 0 {
			start, first = binlogStartPos, nil
		}

		scan, err := scanLog(path, start, first)
		if err != nil {
			return "", err
		}

		// only the source's last write can be torn; anything else is a log we cannot trust.
		if i < len(sourceLogs)-1 {
			name := filepath.Base(path)
			if torn := scan.size - scan.eventsEnd; torn > 0 {
				return "", fmt.Errorf("%w: %s has %d byte(s) after its last complete event", errTornBinlog, name, torn)
			}
			if scan.openTx != nil {
				return "", fmt.Errorf("%w: %s ends inside transaction %s", errTornBinlog, name, scan.openTx)
			}
		}

		gtids.AddSet(scan.gtids)
		scans = append(scans, scan)
	}

	for i, scan := range scans {
		name := filepath.Base(sourceLogs[i])
		if scan.keep == scan.size {
			if scan.openTx != nil {
				log.Printf("WARNING: %s holds none of the rest of transaction %s, so the relay log keeps only its start", name, scan.openTx)
			}
			continue
		}

		if torn := scan.size - scan.eventsEnd; torn > 0 {
			// The source died writing these, so they are a transaction it never
			// committed and never sent anywhere.
			log.Printf("WARNING: dropping %d byte(s) of a partial event from the end of %s", torn, name)
		}
		if scan.openTx != nil {
			log.Printf("WARNING: dropping incomplete transaction %s from the end of %s", scan.openTx, name)
		}
		if scan.stopped {
			log.Printf("Dropping the Stop event from the end of %s: the source shut down cleanly", name)
		}

		if err := os.Truncate(sourceLogs[i], scan.keep); err != nil {
			return "", fmt.Errorf("truncate %s: %w", name, err)
		}
	}

	return gtids.String(), nil
}

// carriedTx is the transaction the receiver was in the middle of when the source
// went away: the relay log holds its GTID event and some of its events, and the
// fetch starts at the rest
type carriedTx struct {
	gtidEvent
	// received is how many of its bytes the relay log already holds
	received int64
}

// receivedPart finds the transaction the relay logs end inside of
func receivedPart(relayLogs relayIndex, applierLog string, sourcePos uint64) (*carriedTx, error) {
	target, err := relayLogs.lastClosed()
	if err != nil {
		return nil, err
	}

	// no transaction the applier has yet to finish starts before its log
	oldest := relayLogs.place(applierLog)
	if oldest < 0 {
		return nil, fmt.Errorf("the index does not list the applier's relay log %q", applierLog)
	}
	for i := relayLogs.place(target); i >= oldest; i-- {
		tx, settled, err := lastRelayedTx(relayLogs[i])
		if err != nil {
			return nil, err
		}
		if !settled {
			continue
		}
		if tx == nil {
			return nil, nil
		}

		// log_pos is 32 bits, so this wraps along with it past 4GiB
		received := int64(uint32(sourcePos) - tx.sourceAt)
		if received >= int64(tx.txLen) {
			return nil, nil
		}
		if received < int64(tx.gtidLen) {
			return nil, fmt.Errorf("%s ends inside transaction %s, which starts at source position %d, but the receiver read up to %d, short of its GTID event",
				filepath.Base(relayLogs[i]), tx, tx.sourceAt, sourcePos)
		}

		return &carriedTx{gtidEvent: tx.gtidEvent, received: received}, nil
	}

	return nil, nil
}

// relayedTx is a transaction a relay log holds the start of
type relayedTx struct {
	gtidEvent
	gtidLen uint32
	// sourceAt is where it starts in the source's binary log, wrapped to 32 bits
	// like log_pos
	sourceAt uint32
}

// lastRelayedTx finds the last transaction path holds the start of; settled is
// false when path says nothing about it, holding no transaction start and no
// sign that the source moved on, and tx is nil when the one there cannot be
// open: it has no GTID, or the source sent a Rotate after it
//
// it reads only the headers of the other events and seeks past large bodies,
// as a relay log may be as large as max_relay_log_size
func lastRelayedTx(path string) (tx *relayedTx, settled bool, err error) {
	name := filepath.Base(path)

	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close() //nolint:errcheck

	if _, err := f.Seek(binlogStartPos, io.SeekStart); err != nil {
		return nil, false, err
	}

	r := bufio.NewReader(f)
	header := make([]byte, eventHeaderLen)
	at := int64(binlogStartPos)
	gtidAt, gtidLen := int64(-1), int64(0)

	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}

			return nil, false, fmt.Errorf("read %s: %w", name, err)
		}

		eventSize := int64(binary.LittleEndian.Uint32(header[9:13]))
		if eventSize < eventHeaderLen {
			break
		}

		switch header[4] {
		case gtidEventType, taggedGTIDEventType:
			gtidAt, gtidLen, settled = at, eventSize, true
		case anonymousGTIDEventType:
			gtidAt, settled = -1, true
		case rotateEventType:
			// the source sends one only between transactions: it moved to its next
			// log, or the receiver reconnected and asks for the transaction again
			if binary.LittleEndian.Uint16(header[17:19])&logEventRelayLogF == 0 {
				gtidAt, settled = -1, true
			}
		}

		at += eventSize
		if body := int(eventSize - eventHeaderLen); body <= r.Buffered() {
			if _, err := r.Discard(body); err != nil {
				return nil, false, fmt.Errorf("read %s: %w", name, err)
			}
		} else {
			if _, err := f.Seek(at, io.SeekStart); err != nil {
				return nil, false, fmt.Errorf("seek %s: %w", name, err)
			}
			r.Reset(f)
		}
	}

	if gtidAt < 0 {
		return nil, settled, nil
	}

	if gtidLen > maxGTIDEventSize {
		return nil, false, fmt.Errorf("%s: GTID event at %d declares %d bytes", name, gtidAt, gtidLen)
	}

	event := make([]byte, gtidLen)
	if _, err := f.ReadAt(event, gtidAt); err != nil {
		return nil, false, fmt.Errorf("read %s: %w", name, err)
	}

	parse := parseGTIDEvent
	if event[4] == taggedGTIDEventType {
		parse = parseTaggedGTIDEvent
	}
	ev, err := parse(event, new(sidCache))
	if err != nil {
		return nil, false, fmt.Errorf("%s at %d: %w", name, gtidAt, err)
	}

	logPos := binary.LittleEndian.Uint32(event[13:17])
	if logPos == 0 {
		return nil, false, fmt.Errorf("%s: GTID event at %d has no source position", name, gtidAt)
	}

	return &relayedTx{gtidEvent: ev, gtidLen: uint32(gtidLen), sourceAt: logPos - uint32(gtidLen)}, true, nil
}

type logScan struct {
	size int64
	// eventsEnd is where the last complete event ends
	eventsEnd int64
	// keep is how much of the log the relay log may take: up to eventsEnd, less a
	// trailing Stop event or a transaction that never got its last bytes.
	keep    int64
	stopped bool
	openTx  *gtidEvent
	// gtids are those of the complete transactions
	gtids gtid.Set
}

// scanLog walks the events in path, which begin at start: a whole binary log
// carries a magic number first. carried, if any, is the transaction the events
// at start belong to.
func scanLog(path string, start int64, carried *carriedTx) (logScan, error) {
	name := filepath.Base(path)

	f, err := os.Open(path)
	if err != nil {
		return logScan{}, err
	}
	defer f.Close() //nolint:errcheck

	fi, err := f.Stat()
	if err != nil {
		return logScan{}, err
	}

	scan := logScan{size: fi.Size(), gtids: make(gtid.Set)}
	if scan.size < start {
		return logScan{}, fmt.Errorf("%s is %d byte(s), shorter than a binary log header", name, scan.size)
	}

	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return logScan{}, err
	}

	r := bufio.NewReader(f)
	header := make([]byte, eventHeaderLen)
	event := make([]byte, maxGTIDEventSize)
	var sids sidCache
	end := start

	var lastType byte
	var lastAt int64
	var lastTx gtidEvent
	var lastTxAt int64

	hasTx, inCarried := carried != nil, carried != nil
	if inCarried {
		lastTx, lastTxAt = carried.gtidEvent, start-carried.received
	}

	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}

			return logScan{}, fmt.Errorf("read %s: %w", name, err)
		}

		eventSize := int64(binary.LittleEndian.Uint32(header[9:13]))
		if eventSize < eventHeaderLen || end+eventSize > scan.size {
			break
		}

		switch header[4] {
		case gtidEventType, taggedGTIDEventType:
			if eventSize > maxGTIDEventSize {
				return logScan{}, fmt.Errorf("%s: GTID event at %d declares %d bytes", name, end, eventSize)
			}

			e := event[:eventSize]
			copy(e, header)
			if _, err := io.ReadFull(r, e[eventHeaderLen:]); err != nil {
				return logScan{}, fmt.Errorf("read %s: %w", name, err)
			}

			parse := parseGTIDEvent
			if header[4] == taggedGTIDEventType {
				parse = parseTaggedGTIDEvent
			}
			ev, err := parse(e, &sids)
			if err != nil {
				return logScan{}, fmt.Errorf("%s at %d: %w", name, end, err)
			}

			// the rest of the carried transaction must be exactly what precedes this
			if inCarried && lastTxAt+int64(lastTx.txLen) != end {
				return logScan{}, fmt.Errorf("%s: the rest of transaction %s should end at %d, but the next transaction starts at %d",
					name, lastTx, lastTxAt+int64(lastTx.txLen), end)
			}
			inCarried = false

			if hasTx {
				scan.gtids.Add(lastTx.sid, lastTx.tag, int64(lastTx.gno))
			}
			lastTx, lastTxAt, hasTx = ev, end, true
		case anonymousGTIDEventType:
			return logScan{}, fmt.Errorf("%w: %s holds an anonymous GTID at %d, so the source does not use GTIDs", errUnsupportedGTID, name, end)
		default:
			if _, err := io.CopyN(io.Discard, r, eventSize-eventHeaderLen); err != nil {
				return logScan{}, fmt.Errorf("read %s: %w", name, err)
			}
		}

		lastType, lastAt = header[4], end
		end += eventSize
	}

	scan.eventsEnd, scan.keep = end, end

	if hasTx {
		if lastTxAt+int64(lastTx.txLen) > end {
			scan.openTx = &lastTx
			scan.keep = max(lastTxAt, start)
		} else {
			scan.gtids.Add(lastTx.sid, lastTx.tag, int64(lastTx.gno))
		}
	}

	// A Stop event is the last thing a source writes before shutting down.
	if lastType == stopEventType && scan.openTx == nil {
		scan.stopped, scan.keep = true, lastAt
	}

	return scan, nil
}
