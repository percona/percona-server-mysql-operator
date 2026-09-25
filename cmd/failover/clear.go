package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/percona/percona-server-mysql-operator/cmd/internal/failover"
)

// clearSource drops the replication channel a promotion left behind.
func clearSource(ctx context.Context, cfg failoverConfig) error {
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
			log.Printf("No replication channel, nothing to clear")
			return nil
		}

		return fmt.Errorf("show replica status: %w", err)
	}

	// A connected receiver is replicating from something real
	// Promoting such a server is orchestrator's call
	if status["Replica_IO_Running"] == "Yes" {
		log.Printf("WARNING: still replicating from %s; leaving the channel alone", status["Source_Host"])
		return nil
	}

	if err := d.StopReplication(ctx); err != nil {
		return fmt.Errorf("stop replica: %w", err)
	}

	if err := d.ResetReplication(ctx); err != nil {
		return fmt.Errorf("reset replica: %w", err)
	}

	log.Printf("Cleared the replication channel left behind by %s", status["Source_Host"])

	return nil
}
