package binlogsource

import (
	"fmt"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/go-mysql-org/go-mysql/server"
)

type Handler struct {
	server.EmptyHandler

	server *Server
}

func (h Handler) UseDB(string) error { return nil }

func (h Handler) HandleQuery(query string) (*mysql.Result, error) {
	return h.server.answer(query)
}

func (h Handler) HandleRegisterSlave([]byte) error { return nil }

func (h Handler) HandleBinlogDump(pos mysql.Position) (*replication.BinlogStreamer, error) {
	return nil, fmt.Errorf("not supported")
}

func (h Handler) HandleBinlogDumpGTID(replicaSet *mysql.MysqlGTIDSet) (*replication.BinlogStreamer, error) {
	idx, err := ReadIndex(h.server.cfg.IndexPath)
	if err != nil {
		return nil, err
	}
	start, err := StartFile(idx, replicaSet)
	if err != nil {
		return nil, err
	}

	from := 0
	for i, f := range idx.Files {
		if f == start {
			from = i
			break
		}
	}

	streamer := replication.NewBinlogStreamer()
	go func() {
		p := replication.NewBinlogParser()
		p.SetRawMode(true)
		for _, f := range idx.Files[from:] {
			err := p.ParseFile(f, 0, func(e *replication.BinlogEvent) error {
				return streamer.AddEventToStreamer(e)
			})
			// A torn tail on the final file is expected; stop cleanly.
			if err != nil {
				if !isTruncated(err) {
					streamer.AddErrorToStreamer(err)
				}
				return
			}
		}
	}()
	return streamer, nil
}
