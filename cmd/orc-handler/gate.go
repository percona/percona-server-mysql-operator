package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"

	failoverlock "github.com/percona/percona-server-mysql-operator/cmd/internal/failover"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

const (
	gateLock = "failover.lock"
	claimDir = "claim"
	seenDir  = "seen"

	// claimIdle is how long a claim outlives its last heartbeat. The hook beats
	// while it runs, so a claim nobody touched for this long belongs to a
	// recovery whose post hook never came: orchestrator lost it, to a crash or
	// a leadership change, between the hook and the promotion. Long enough to
	// outlast the regroup that follows the hook, short enough not to strand the
	// cluster.
	claimIdle = orchestrator.RecoveryClaimIdle
	claimBeat = 10 * time.Second

	// seenIdle bounds how long a first-seen mark outlives the attempts that
	// refresh it. Orchestrator reports a failure that persists every few
	// seconds, so a mark nobody refreshed for this long belongs to a failure
	// that ended without the hook noticing, such as a cluster repaired by hand. Without the bound the next, unrelated failure
	// of the same source would start with its timeout already spent.
	seenIdle = time.Hour

	notifiedDir = "notified"

	// notifyInterval keeps a blocked failover, which orchestrator retries every
	// few seconds, from turning into a flood of identical events.
	notifyInterval = 5 * time.Minute
)

var errClaimed = errors.New("another recovery of this source is in flight")

// gate lets one orchestrator recovery per dead source past the hook at a time.
//
// Orchestrator registers a fresh recovery for a failure that persists as soon
// as the previous one is over or acknowledged, and each one runs this hook. Every recovery that
// gets a successful hook goes on to pick a candidate and promote it, so letting
// a second one through while the first is between its hook and its promotion
// ends with two promotions, and they need not agree.
//
// A claim on the source is taken at hook entry and held until the recovery's
// post hook releases it, or until the hook exits non-zero, which orchestrator
// answers by abandoning that recovery without a post hook. A hook that finds
// the source claimed fails at once: orchestrator drops its recovery and
// registers another one later.
type gate struct {
	dir       string
	claimIdle time.Duration
	beat      time.Duration
	idle      time.Duration
}

func newGate() *gate {
	return &gate{dir: orchestrator.HandlerStateDir, claimIdle: claimIdle, beat: claimBeat, idle: seenIdle}
}

func (g *gate) enter() (func(), error) {
	if err := os.MkdirAll(g.dir, 0o755); err != nil {
		return nil, errors.Wrapf(err, "create %s", g.dir)
	}

	f, err := failoverlock.Lock(filepath.Join(g.dir, gateLock))
	if err != nil {
		return nil, err
	}

	return func() { f.Close() }, nil //nolint:errcheck
}

// claim takes the source for the recovery uid, or fails with errClaimed while
// another recovery holds it. A claim gone idle is taken over.
func (g *gate) claim(source, uid string) error {
	release, err := g.enter()
	if errors.Is(err, failoverlock.ErrLocked) {
		return errClaimed
	}
	if err != nil {
		return err
	}
	defer release()

	holder, age, ok, err := g.holder(source)
	if err != nil {
		return err
	}
	if ok && age < g.claimIdle {
		return errors.Wrapf(errClaimed, "recovery %q has held %s for %s", holder, source, age.Truncate(time.Second))
	}

	return g.write(claimDir, source, []byte(uid))
}

// holder returns the recovery holding the claim on source and how long ago it
// last beat, or false when there is no claim.
func (g *gate) holder(source string) (string, time.Duration, bool, error) {
	age, ok, err := g.markAge(claimDir, source)
	if err != nil || !ok {
		return "", 0, false, err
	}

	path, err := g.markPath(claimDir, source)
	if err != nil {
		return "", 0, false, err
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", 0, false, errors.Wrapf(err, "read %s", path)
	}

	return strings.TrimSpace(string(content)), age, true, nil
}

// keepClaim beats the claim on source until the returned func is called,
// which also waits for the last beat to land. The hook keeps its claim for as
// long as it runs, so only a claim whose hook has exited can go idle.
func (g *gate) keepClaim(ctx context.Context, source string) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)

		ticker := time.NewTicker(g.beat)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := g.touch(claimDir, source); err != nil {
					log.Error(err, "failed to keep the claim", "source", source)
				}
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

// release drops the claim on source if uid holds it, and reports whether it
// did. A claim held by another recovery is left alone: that one took over
// after this one went idle, and it is the one still in flight.
func (g *gate) release(source, uid string) (bool, error) {
	release, err := g.enter()
	if err != nil {
		return false, err
	}
	defer release()

	holder, _, ok, err := g.holder(source)
	if err != nil || !ok {
		return false, err
	}
	if holder != uid {
		return false, nil
	}

	path, err := g.markPath(claimDir, source)
	if err != nil {
		return false, err
	}

	return true, errors.Wrapf(ignoreNotExist(os.Remove(path)), "remove %s", path)
}

// markPath refuses anything that is not a bare host name. The source comes from
// orchestrator's {failedHost} substitution and is used as a file name.
func (g *gate) markPath(dir, source string) (string, error) {
	if source == "" || source == "." || source == ".." || strings.ContainsRune(source, filepath.Separator) {
		return "", errors.Errorf("%q is not a host name", source)
	}

	return filepath.Join(g.dir, dir, source), nil
}

// markAge returns how long ago the mark was last touched, or false when there
// is no such mark.
func (g *gate) markAge(dir, source string) (time.Duration, bool, error) {
	path, err := g.markPath(dir, source)
	if err != nil {
		return 0, false, err
	}

	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, errors.Wrapf(err, "stat %s", path)
	}

	return time.Since(fi.ModTime()), true, nil
}

// firstSeen returns when the failure of this source was first reported. The
// failover timeout is measured from it, so it spans every retry orchestrator
// makes rather than a single attempt: the attempts that leave a cluster stuck
// are the ones that fail in under a second, over and over.
func (g *gate) firstSeen(source string) (time.Time, bool, error) {
	age, ok, err := g.markAge(seenDir, source)
	if err != nil || !ok {
		return time.Time{}, false, err
	}

	// The mark's mtime is the last attempt, its content the first one.
	if age > g.idle {
		return time.Time{}, false, g.clearSeen(source)
	}

	path, err := g.markPath(seenDir, source)
	if err != nil {
		return time.Time{}, false, err
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, false, errors.Wrapf(err, "read %s", path)
	}

	first, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(content)))
	if err != nil {
		// Not a mark this binary wrote. Starting over beats timing a failure
		// from garbage.
		return time.Time{}, false, g.clearSeen(source)
	}

	return first, true, nil
}

// markSeen records an attempt at this source and returns when the first one
// was made, and whether this is it. The first attempt starts the clock; later
// ones leave it alone and only keep the mark from going idle, which matters
// because an attempt may spend most of the budget waiting before it gives up.
func (g *gate) markSeen(source string) (time.Time, bool, error) {
	first, ok, err := g.firstSeen(source)
	if err != nil {
		return time.Time{}, false, err
	}
	if ok {
		return first, false, g.touch(seenDir, source)
	}

	now := time.Now()

	return now, true, g.write(seenDir, source, []byte(now.Format(time.RFC3339Nano)))
}

// refreshSeen keeps an existing mark from going idle. An attempt may itself
// outlast the idle bound, so it cannot go through markSeen on the way out:
// that would take its own mark for a stale one and restart the clock.
func (g *gate) refreshSeen(source string) error {
	path, err := g.markPath(seenDir, source)
	if err != nil {
		return err
	}

	now := time.Now()

	return errors.Wrapf(ignoreNotExist(os.Chtimes(path, now, now)), "touch %s", path)
}

func (g *gate) clearSeen(source string) error {
	path, err := g.markPath(seenDir, source)
	if err != nil {
		return err
	}

	return errors.Wrapf(ignoreNotExist(os.Remove(path)), "remove %s", path)
}

// clearAllSeen drops every first-seen mark. A promotion means the cluster has a
// primary again, so nothing that came before it is still being timed.
func (g *gate) clearAllSeen() error {
	path := filepath.Join(g.dir, seenDir)

	return errors.Wrapf(ignoreNotExist(os.RemoveAll(path)), "remove %s", path)
}

// notifyOnce reports whether the caller should record an event of this reason
// for this source, and marks it so the next retries stay quiet for
// notifyInterval. Reasons are tracked apart: a failover that starts waiting
// and times out within the interval reports both.
func (g *gate) notifyOnce(source, reason string) (bool, error) {
	age, ok, err := g.markAge(notifiedPath(reason), source)
	if err != nil {
		return false, err
	}
	if ok && age < notifyInterval {
		return false, nil
	}

	return true, g.markNotified(source, reason)
}

// markNotified keeps notify quiet about this reason for this source for
// notifyInterval.
func (g *gate) markNotified(source, reason string) error {
	return g.touch(notifiedPath(reason), source)
}

func notifiedPath(reason string) string {
	return filepath.Join(notifiedDir, reason)
}

func (g *gate) touch(dir, source string) error {
	return g.write(dir, source, nil)
}

// write creates the mark with the given content, replacing what a previous
// one held, or only bumps its mtime when it already exists and there is
// nothing to write.
func (g *gate) write(dir, source string, content []byte) error {
	path, err := g.markPath(dir, source)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errors.Wrapf(err, "create %s", filepath.Dir(path))
	}

	flags := os.O_CREATE | os.O_WRONLY
	if content != nil {
		flags |= os.O_TRUNC
	}

	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return errors.Wrapf(err, "create %s", path)
	}
	if _, err := f.Write(content); err != nil {
		f.Close() //nolint:errcheck
		return errors.Wrapf(err, "write %s", path)
	}
	if err := f.Close(); err != nil {
		return errors.Wrapf(err, "close %s", path)
	}

	// A second failover for the same source has to restart the clock rather than
	// inherit the age of the first one's mark.
	now := time.Now()

	return errors.Wrapf(os.Chtimes(path, now, now), "touch %s", path)
}

func ignoreNotExist(err error) error {
	if os.IsNotExist(err) {
		return nil
	}

	return err
}
