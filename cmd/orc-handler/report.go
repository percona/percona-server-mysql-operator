package main

import (
	"context"
	"flag"

	"github.com/pkg/errors"

	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

// runReportFailover records the recoveries orchestrator gives up on
func runReportFailover(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("report-failover", flag.ExitOnError)
	source := fs.String("source", "", "Hostname of the server whose recovery failed")
	failureType := fs.String("failure-type", "", "Analysis code orchestrator reported the problem as")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *source == "" {
		return errors.New("source flag should not be empty")
	}

	if !orchestrator.IsMasterFailover(*failureType) {
		log.Info("Recovery does not promote a replica, nothing to report", "failureType", *failureType)
		return nil
	}

	log.Info("Orchestrator could not recover from the failure", "source", *source, "failureType", *failureType)

	return notify(ctx, newGate(), *source, naming.EventFailoverFailed,
		"Orchestrator could not recover from %s on %s. The cluster may be left without a writable primary.",
		*failureType, *source)
}
