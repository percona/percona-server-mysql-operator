package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/cmd/bootstrap/utils"
	"github.com/percona/percona-server-mysql-operator/cmd/internal/db"
)

const (
	sourceProbePoll    = 5 * time.Second
	sourceProbeTimeout = 5 * time.Second

	// sourceConfirmations is how many probes in a row the source has to answer.
	// A source that is really gone costs one refused connection, so this only
	// slows down the case where we are about to skip the failover anyway.
	sourceConfirmations = 2

	// receiverWait bounds the wait for the receiver to reconnect after a
	// stand-down. Missing it is logged, never fatal.
	receiverWait = 20 * time.Second
)

var errSourceRecovered = errors.New("the source is back; standing down without promoting")

// sourceDatabase is the failed source seen from the replica we splice into.
type sourceDatabase interface {
	GetGTIDExecuted(ctx context.Context) (string, error)
	Close() error
}

// sourceWatch reports whether the source that orchestrator gave up on is usable
// again, so the job can hand the replica back instead of promoting it.
//
// It probes the source's FQDN rather than the pod IP the binary logs are fetched
// from. The mysql headless service does not publish not-ready addresses, so a
// name that resolves means the pod passed its readiness probe, which means its
// startup probe - the bootstrap - has already settled the pod's own view of the
// topology. It is also the host the replication channel is configured with, so
// it is what the receiver has to reach to reconnect.
type sourceWatch struct {
	local        database
	connect      func(ctx context.Context, host string) (sourceDatabase, error)
	host         string
	poll         time.Duration
	timeout      time.Duration
	receiverWait time.Duration

	seen     int
	reported bool
}

func newSourceWatch(cfg failoverConfig, local database, host string) *sourceWatch {
	return &sourceWatch{
		local:        local,
		connect:      cfg.newSourceDB,
		host:         host,
		poll:         cfg.sourcePoll,
		timeout:      cfg.sourceTimeout,
		receiverWait: cfg.receiverWait,
	}
}

// confirmed blocks until the source has answered sourceConfirmations probes in a
// row, or until one of them fails.
func (w *sourceWatch) confirmed(ctx context.Context) bool {
	if w.connect == nil || w.host == "" {
		return false
	}

	for {
		if !w.probe(ctx) {
			return false
		}
		if w.seen >= sourceConfirmations {
			log.Printf("Source %s is back and holds everything we do", w.host)
			return true
		}

		select {
		case <-ctx.Done():
			return false
		case <-time.After(w.poll):
		}
	}
}

// probe reports whether the source answered this time.
func (w *sourceWatch) probe(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()

	if err := w.usable(ctx); err != nil {
		// The probe runs for as long as the drain does, so only the failure
		// that broke a streak is worth a line.
		if w.seen > 0 || !w.reported {
			log.Printf("Not standing down: %v", err)
			w.reported = true
		}
		w.seen = 0

		return false
	}

	w.seen++
	w.reported = false

	return true
}

// usable reports whether the source is answering and holds every transaction
// this replica has executed.
func (w *sourceWatch) usable(ctx context.Context) error {
	d, err := w.connect(ctx, w.host)
	if err != nil {
		return fmt.Errorf("source %s is unreachable: %w", w.host, err)
	}
	defer d.Close() //nolint:errcheck

	sourceSet, err := d.GetGTIDExecuted(ctx)
	if err != nil {
		return fmt.Errorf("get source GTID_EXECUTED: %w", err)
	}

	localSet, err := w.local.GetGTIDExecuted(ctx)
	if err != nil {
		return fmt.Errorf("get GTID_EXECUTED: %w", err)
	}

	// Handing the replica back would strand these for good: they carry the
	// source's own UUID, so replicate-same-server-id filters them out and
	// nothing ever delivers them back. Promoting is the only way to keep them.
	ahead, err := w.local.GTIDSubtract(ctx, localSet, sourceSet)
	if err != nil {
		return fmt.Errorf("compare GTID sets: %w", err)
	}
	if ahead != "" {
		return fmt.Errorf("we hold transactions the source lost: %s", ahead)
	}

	return nil
}

// watchSource closes the returned channel once, and only once, the source is
// confirmed back. A watch that is torn down leaves it open: closing it there
// would read as a recovered source and cancel the promotion.
func watchSource(ctx context.Context, w *sourceWatch) <-chan struct{} {
	recovered := make(chan struct{})

	go func() {
		ticker := time.NewTicker(w.poll)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			if w.confirmed(ctx) {
				close(recovered)
				return
			}
		}
	}()

	return recovered
}

// standDown hands the replica back to the source that came back. The splice
// stays where it is: the applier keeps working through it out of the relay log
// while the receiver re-requests the overlapping range, and auto-position skips
// whatever has already been executed by the time it arrives.
func (w *sourceWatch) standDown(ctx context.Context) error {
	if err := w.local.StartIOThread(ctx); err != nil {
		return fmt.Errorf("start IO_THREAD: %w", err)
	}
	log.Printf("Started IO_THREAD")

	waitForReceiver(ctx, w.local, w.poll, w.receiverWait)

	return errSourceRecovered
}

// waitForReceiver gives the receiver a moment to reconnect before the job exits
// and drops the lock that keeps the readiness probe quiet about the stopped
// replication. Only ever logs: the job is standing down either way.
func waitForReceiver(ctx context.Context, s replicaStatuser, poll, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		status, err := s.ShowReplicaStatus(ctx)
		switch {
		case err != nil:
			log.Printf("WARNING: cannot tell whether the receiver reconnected: %v", err)
			return
		case status["Replica_IO_Running"] == "Yes":
			log.Printf("Receiver reconnected to %s", status["Source_Host"])
			return
		}

		select {
		case <-ctx.Done():
			log.Printf("WARNING: the receiver has not reconnected within %s; the pod stays NotReady until it does", timeout)
			return
		case <-ticker.C:
		}
	}
}

func connectToSource(ctx context.Context, host string) (sourceDatabase, error) {
	operatorPass, err := utils.GetSecret(apiv1.UserOperator)
	if err != nil {
		return nil, fmt.Errorf("get %s password: %w", apiv1.UserOperator, err)
	}

	return db.NewDatabase(ctx, db.DBParams{
		User: apiv1.UserOperator,
		Pass: operatorPass,
		Host: host,
	})
}
