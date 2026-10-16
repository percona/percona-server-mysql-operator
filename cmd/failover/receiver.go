package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/percona/percona-server-mysql-operator/cmd/internal/failover"
)

// startReceiver starts a receiver that spent its connect retries while the
// source was down. nothing else restarts it once a failover stands down: the
// source is back, so orchestrator has nothing to recover
func startReceiver(ctx context.Context, cfg failoverConfig) error {
	host, err := sourceHost(cfg.source)
	if err != nil {
		return err
	}

	lock, err := failover.Lock(cfg.lockPath)
	if err != nil {
		return err
	}
	defer lock.Close() //nolint:errcheck

	d, err := cfg.newDatabase(ctx)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}

	defer func() {
		if err := d.Close(); err != nil {
			log.Printf("ERROR: failed to close database connection: %v", err)
		}
	}()

	status, err := d.ShowReplicaStatus(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			log.Printf("No replication channel, no receiver to start")
			return nil
		}

		return fmt.Errorf("show replica status: %w", err)
	}

	if status["Source_Host"] != host {
		log.Printf("Replicating from %s rather than %s; leaving the receiver alone", status["Source_Host"], host)
		return nil
	}

	// a Connecting receiver is still retrying on its own
	if status["Replica_IO_Running"] != "No" {
		log.Printf("Receiver is %s; leaving it alone", status["Replica_IO_Running"])
		return nil
	}

	if err := d.StartIOThread(ctx); err != nil {
		return fmt.Errorf("start IO_THREAD: %w", err)
	}

	log.Printf("Started the receiver for %s", host)

	return nil
}
