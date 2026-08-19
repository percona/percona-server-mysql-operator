package binlogsource

import (
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/go-mysql-org/go-mysql/server"
	"github.com/pkg/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// streamerBufferSize is how many events may sit between the goroutine reading the
// logs and the connection writing them out. go-mysql defaults to 10240, which on wide
// rows events is tens of megabytes held per connection.
const streamerBufferSize = 512

type handler struct {
	server.EmptyHandler

	server *Server

	// ctx ends the dump when the connection does. go-mysql reads the stream with a
	// background context, so the only way out is a stream error.
	ctx context.Context

	// heartbeat is the period the replica asked for, in nanoseconds.
	heartbeat atomic.Int64
}

func newHandler(ctx context.Context, s *Server) *handler {
	return &handler{server: s, ctx: ctx}
}

func (h *handler) UseDB(string) error { return nil }

func (h *handler) HandleQuery(query string) (*mysql.Result, error) {
	h.noteHeartbeatPeriod(query)
	return h.server.answer(query)
}

// A replica sends "SET @master_heartbeat_period = <ns>, @source_heartbeat_period =
// <ns>" during the handshake, or nothing at all when heartbeats are off. Both
// variables carry the same value, so the first one settles it.
var heartbeatPeriod = regexp.MustCompile(`(?i)heartbeat_period\s*=\s*(\d+)`)

func (h *handler) noteHeartbeatPeriod(query string) {
	m := heartbeatPeriod.FindStringSubmatch(query)
	if m == nil {
		return
	}

	ns, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		logf.FromContext(h.ctx).Error(err, "cannot read heartbeat period", "query", query)
		return
	}
	h.heartbeat.Store(ns)
}

func (h *handler) HandleRegisterSlave([]byte) error { return nil }

func (h *handler) HandleBinlogDump(pos mysql.Position) (*replication.BinlogStreamer, error) {
	// A replica only asks for this with GTIDs off, and the error it gets back reaches
	// nothing but its own error log.
	err := errors.New("only GTID based replication is supported")
	logf.FromContext(h.ctx).Error(err, "replica asked for a file and position dump",
		"file", pos.Name, "position", pos.Pos)
	return nil, err
}

func (h *handler) HandleBinlogDumpGTID(replicaSet *mysql.MysqlGTIDSet) (*replication.BinlogStreamer, error) {
	log := logf.FromContext(h.ctx)

	logs, err := h.server.index()
	if err != nil {
		log.Error(err, "failed to read the binary log index")
		return nil, err
	}

	// Start at the newest file the replica already has the history for, so it is not
	// sent transactions it has applied already.
	files, err := remaining(logs, replicaSet)
	if err != nil {
		log.Error(err, "found no binary log to start the replica from",
			"replicaGTIDSet", replicaSet.String(), "logs", len(logs))
		return nil, err
	}

	log.Info("serving binary logs to the replica",
		"replicaGTIDSet", replicaSet.String(),
		"from", filepath.Base(files[0]),
		"files", len(files))

	streamer := replication.NewBinlogStreamerWithChanSize(streamerBufferSize)

	// A cancelled context has to reach the stream as an error: it is the only thing
	// that unblocks both the writer reading the stream and the goroutine below.
	go func() {
		<-h.ctx.Done()
		streamer.AddErrorToStreamer(h.ctx.Err())
	}()

	go func() {
		var served *binlogScan

		for _, f := range files {
			sc, err := h.serveFile(streamer, f)
			if err != nil {
				// A torn down source ends every dump it holds; the connection reports
				// that on its own.
				if h.ctx.Err() == nil {
					log.Error(err, "failed to serve binary log", "file", filepath.Base(f))
				}
				streamer.AddErrorToStreamer(err)
				return
			}
			served = sc
		}

		if served != nil {
			// What the new primary was handed, and whether the connection is held open
			// for it afterwards.
			log.Info("served every binary log to the replica",
				"file", filepath.Base(served.file),
				"position", served.committed,
				"heartbeat", time.Duration(h.heartbeat.Load()).String())
		}

		h.keepAlive(streamer, served)
	}()

	return streamer, nil
}

// serveFile announces one binary log and streams every committed event in it. The old
// primary may have died part way through a transaction, so the scan stops the stream
// at its last commit rather than serve what nothing will ever finish.
func (h *handler) serveFile(s *replication.BinlogStreamer, file string) (*binlogScan, error) {
	sc, err := h.server.scanOf(file)
	if err != nil {
		return nil, err
	}

	if sc.committed < sc.complete {
		// The primary died part way through writing a transaction. Nothing will ever
		// finish it, and this is what explains its absence on the new primary.
		logf.FromContext(h.ctx).Info("binary log ends in an unfinished transaction",
			"file", filepath.Base(file), "servingUpTo", sc.committed, "length", sc.complete)
	}

	rotate := fakeRotateEvent(h.server.cfg.ServerID, file, binlogFileHeaderSize, sc.checksum)
	if err := s.AddEventToStreamer(rotate); err != nil {
		return nil, err
	}

	err = parseBinlog(file, true, func(e *replication.BinlogEvent, pos int64, _ []byte) error {
		sanitizeFormatDescription(e, sc.checksum)
		if err := s.AddEventToStreamer(e); err != nil {
			return err
		}
		if pos >= sc.committed {
			return errStopParse
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return sc, nil
}

// keepAlive holds the connection open once everything has been served. Nothing more
// will ever be written to a fenced primary's logs, but a replica drops a connection
// that has gone quiet for replica_net_timeout and reconnects in a loop.
func (h *handler) keepAlive(s *replication.BinlogStreamer, served *binlogScan) {
	period := time.Duration(h.heartbeat.Load())
	if served == nil || period <= 0 {
		// Nothing to send: a heartbeat names the log it has reached. Leave the stream
		// idle rather than end it -- GetEvent selects over the event and error channels
		// at once, so an error here races the events still buffered ahead of it. The
		// context tears the connection down when the source is stopped.
		return
	}

	tick := time.NewTicker(period)
	defer tick.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-tick.C:
			beat := heartbeatEvent(h.server.cfg.ServerID, served.file, served.committed, served.checksum)
			if err := s.AddEventToStreamer(beat); err != nil {
				return
			}
		}
	}
}
