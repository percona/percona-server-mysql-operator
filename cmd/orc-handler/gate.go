package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"

	failoverlock "github.com/percona/percona-server-mysql-operator/cmd/internal/failover"
)

const (
	gateDir    = "/tmp/orc-handler"
	gateLock   = "failover.lock"
	handledDir = "handled"

	// gateWait is how long an attempt waits for the one in flight. Giving up
	// aborts only that attempt's recovery, which costs nothing: the hook runs
	// before orchestrator touches the topology.
	gateWait = time.Minute
	gatePoll = 250 * time.Millisecond

	// handledTTL is how long a completed failover suppresses another one for the
	// same source.
	handledTTL = 2 * time.Minute
)

// gate serializes the failovers this binary runs and remembers the sources it
// has already handled. Orchestrator starts a fresh recovery every
// RecoveryPeriodBlockSeconds while the hook of the first one is still running,
// and each of those picks its own promotion candidate: without the gate they
// splice into different pods at the same time.
type gate struct {
	dir  string
	ttl  time.Duration
	wait time.Duration
	poll time.Duration
}

func newGate() *gate {
	return &gate{dir: gateDir, ttl: handledTTL, wait: gateWait, poll: gatePoll}
}

func (g *gate) enter(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(g.dir, 0o755); err != nil {
		return nil, errors.Wrapf(err, "create %s", g.dir)
	}

	f, err := failoverlock.LockWait(ctx, filepath.Join(g.dir, gateLock), g.wait, g.poll)
	if err != nil {
		return nil, err
	}

	return func() { f.Close() }, nil //nolint:errcheck
}

// markPath refuses anything that is not a bare host name. The source comes from
// orchestrator's {failedHost} substitution and is used as a file name.
func (g *gate) markPath(source string) (string, error) {
	if source == "" || source == "." || source == ".." || strings.ContainsRune(source, filepath.Separator) {
		return "", errors.Errorf("%q is not a host name", source)
	}

	return filepath.Join(g.dir, handledDir, source), nil
}

func (g *gate) handled(source string) (bool, error) {
	path, err := g.markPath(source)
	if err != nil {
		return false, err
	}

	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, errors.Wrapf(err, "stat %s", path)
	}

	return time.Since(fi.ModTime()) < g.ttl, nil
}

func (g *gate) markHandled(source string) error {
	path, err := g.markPath(source)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errors.Wrapf(err, "create %s", filepath.Dir(path))
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return errors.Wrapf(err, "create %s", path)
	}
	if err := f.Close(); err != nil {
		return errors.Wrapf(err, "close %s", path)
	}

	// A second failover for the same source has to restart the clock rather than
	// inherit the age of the first one's mark.
	now := time.Now()

	return errors.Wrapf(os.Chtimes(path, now, now), "touch %s", path)
}
