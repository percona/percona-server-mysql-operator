package binlogsource

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/go-mysql-org/go-mysql/server"
	"github.com/pkg/errors"
)

// errEndOfFile stops the parser once the last whole event has been served.
var errEndOfFile = errors.New("end of binary log")

type Handler struct {
	server.EmptyHandler

	server *Server

	// ctx ends the dump when the connection does. go-mysql reads the stream
	// with a background context, so the only way out is a stream error.
	ctx context.Context

	// heartbeat is the period the replica asked for, in nanoseconds.
	heartbeat *atomic.Int64
}

func newHandler(ctx context.Context, s *Server) Handler {
	return Handler{server: s, ctx: ctx, heartbeat: new(atomic.Int64)}
}

func (h Handler) UseDB(string) error { return nil }

func (h Handler) HandleQuery(query string) (*mysql.Result, error) {
	h.noteHeartbeatPeriod(query)
	return h.server.answer(query)
}

// noteHeartbeatPeriod picks the heartbeat period out of the handshake. A replica
// sends "SET @master_heartbeat_period = <ns>, @source_heartbeat_period = <ns>",
// and sends nothing at all when heartbeats are turned off.
func (h Handler) noteHeartbeatPeriod(query string) {
	const name = "heartbeat_period"

	q := strings.ToLower(query)
	if !strings.HasPrefix(strings.TrimSpace(q), "set ") || !strings.Contains(q, name) {
		return
	}

	// Both variables carry the same value, so the first one settles it.
	_, rest, found := strings.Cut(q, name)
	if !found {
		return
	}
	_, rest, found = strings.Cut(rest, "=")
	if !found {
		return
	}

	value := strings.TrimSpace(rest)
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == 0 {
		log.Printf("ERROR: no heartbeat period in %q", query)
		return
	}

	ns, err := strconv.ParseInt(value[:end], 10, 64)
	if err != nil {
		log.Printf("ERROR: cannot read heartbeat period from %q: %v", query, err)
		return
	}
	h.heartbeat.Store(ns)
}

func (h Handler) HandleRegisterSlave([]byte) error { return nil }

func (h Handler) HandleBinlogDump(pos mysql.Position) (*replication.BinlogStreamer, error) {
	return nil, fmt.Errorf("not supported")
}

func (h Handler) HandleBinlogDumpGTID(replicaSet *mysql.MysqlGTIDSet) (*replication.BinlogStreamer, error) {
	log.Printf("HandleBinlogDumpGTID replicaSet: %v", replicaSet)
	idx, err := ReadIndex(h.server.cfg.IndexPath)
	if err != nil {
		return nil, err
	}

	// Start at the newest file the replica already has the history for, so it is
	// not sent transactions it has applied already.
	start, err := StartFile(idx, replicaSet)
	if err != nil {
		return nil, err
	}

	streamer := replication.NewBinlogStreamer()

	// A cancelled context has to reach the stream as an error: it is the only
	// thing that unblocks both the writer reading the stream and the goroutine
	// below filling it.
	go func() {
		<-h.ctx.Done()
		streamer.AddErrorToStreamer(h.ctx.Err())
	}()

	go func() {
		var (
			served   string
			servedTo int64
			checksum bool
		)

		p := replication.NewBinlogParser()
		p.SetRawMode(true)
		for _, f := range idx.Since(start) {
			checksum, err = fileHasChecksum(f)
			if err != nil {
				streamer.AddErrorToStreamer(err)
				return
			}
			// The old primary may have died part way through a transaction, so
			// stop at its last commit rather than read into what nothing will
			// ever finish.
			complete, err := committedLength(f, checksum)
			if err != nil {
				streamer.AddErrorToStreamer(err)
				return
			}

			// A source announces every file before streaming it, so the replica
			// knows which of its logs the events that follow came from.
			rotate := fakeRotateEvent(h.server.cfg.ServerID, f, binlogFileHeaderSize, checksum)
			if err := streamer.AddEventToStreamer(rotate); err != nil {
				return
			}

			err = p.ParseFile(f, 0, func(e *replication.BinlogEvent) error {
				sanitizeFormatDescription(e, checksum)
				if err := streamer.AddEventToStreamer(e); err != nil {
					return err
				}
				// LogPos is the offset just past the event in the file.
				if int64(e.Header.LogPos) >= complete {
					return errEndOfFile
				}
				return nil
			})
			if err != nil && errors.Cause(err) != errEndOfFile {
				streamer.AddErrorToStreamer(err)
				return
			}

			served, servedTo = f, complete
		}

		h.keepAlive(streamer, served, uint32(servedTo), checksum)
	}()

	return streamer, nil
}

// keepAlive holds the connection open once everything has been served. The old
// primary is fenced, so nothing more will ever be written to its logs, but a
// replica drops a connection that has gone quiet for replica_net_timeout and
// reconnects in a loop. Heartbeats keep the connection quiet without letting it
// look dead, so the operator sees no replication error while it waits for the
// replica to apply what it has.
func (h Handler) keepAlive(s *replication.BinlogStreamer, file string, pos uint32, checksum bool) {
	period := time.Duration(h.heartbeat.Load())
	if period <= 0 {
		// The replica turned heartbeats off, so there is nothing to send it and
		// no way to keep the connection healthy. Leave the stream idle rather
		// than end it: an error on the stream races the events still buffered
		// ahead of it, because GetEvent selects over both channels at once, so
		// ending the dump here could cut it short. The context tears the
		// connection down when the source is stopped.
		return
	}

	tick := time.NewTicker(period)
	defer tick.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-tick.C:
			beat := heartbeatEvent(h.server.cfg.ServerID, file, pos, checksum)
			if err := s.AddEventToStreamer(beat); err != nil {
				return
			}
		}
	}
}
