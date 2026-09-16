package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSourceDB struct {
	gtid string
	conn *fakeSourceConn
}

var _ sourceDatabase = (*fakeSourceDB)(nil)

func (f *fakeSourceDB) GetGTIDExecuted(context.Context) (string, error) {
	return f.gtid, nil
}

func (f *fakeSourceDB) Close() error {
	f.conn.mu.Lock()
	defer f.conn.mu.Unlock()

	f.conn.closes++

	return nil
}

// fakeSourceConn dials the source. up is consumed one entry per dial and its
// last entry repeats, so a one-element script describes a steady state.
type fakeSourceConn struct {
	mu     sync.Mutex
	up     []bool
	gtid   string
	dials  int
	closes int
	hosts  []string
}

func (c *fakeSourceConn) connect(_ context.Context, host string) (sourceDatabase, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.hosts = append(c.hosts, host)
	i := c.dials
	c.dials++

	if len(c.up) == 0 {
		return nil, errors.New("dial tcp: connect: connection refused")
	}
	if !c.up[min(i, len(c.up)-1)] {
		return nil, errors.New("dial tcp: connect: connection refused")
	}

	return &fakeSourceDB{gtid: c.gtid, conn: c}, nil
}

func (c *fakeSourceConn) count() (dials, closes int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.dials, c.closes
}

func newWatch(t *testing.T, conn *fakeSourceConn, local database) *sourceWatch {
	t.Helper()

	return newSourceWatch(failoverConfig{
		newSourceDB:   conn.connect,
		sourcePoll:    time.Millisecond,
		sourceTimeout: time.Second,
		receiverWait:  50 * time.Millisecond,
	}, local, "mysql-0.mysql")
}

func TestSourceWatch(t *testing.T) {
	t.Run("a source that answers every probe is confirmed", func(t *testing.T) {
		conn := &fakeSourceConn{up: []bool{true}}
		w := newWatch(t, conn, &fakeDatabase{})

		assert.True(t, w.confirmed(t.Context()))

		dials, closes := conn.count()
		assert.Equal(t, sourceConfirmations, dials, "one dial per confirmation")
		assert.Equal(t, dials, closes, "every probe must close its connection")
	})

	t.Run("a dead source is not confirmed and costs one dial", func(t *testing.T) {
		conn := &fakeSourceConn{}
		w := newWatch(t, conn, &fakeDatabase{})

		assert.False(t, w.confirmed(t.Context()))

		dials, _ := conn.count()
		assert.Equal(t, 1, dials, "a refused connection must not be retried within a run")
	})

	t.Run("a source that answers once and dies is not confirmed", func(t *testing.T) {
		conn := &fakeSourceConn{up: []bool{true, false}}
		w := newWatch(t, conn, &fakeDatabase{})

		assert.False(t, w.confirmed(t.Context()), "one answer is not enough to hand the replica back")
	})

	t.Run("the streak has to be consecutive", func(t *testing.T) {
		conn := &fakeSourceConn{up: []bool{true, false, true, true}}
		w := newWatch(t, conn, &fakeDatabase{})

		assert.False(t, w.confirmed(t.Context()))
		assert.True(t, w.confirmed(t.Context()), "the two answers after the failure confirm it")
	})

	t.Run("a source missing transactions we hold is refused", func(t *testing.T) {
		conn := &fakeSourceConn{up: []bool{true}}
		local := &fakeDatabase{gtidAhead: "b7b097e0-1111-1111-1111-111111111111:50147-123853"}
		w := newWatch(t, conn, local)

		assert.False(t, w.confirmed(t.Context()),
			"standing down would strand transactions nothing can replicate back")
	})

	t.Run("an unreadable GTID set is refused", func(t *testing.T) {
		conn := &fakeSourceConn{up: []bool{true}}
		w := newWatch(t, conn, &fakeDatabase{gtidErr: errors.New("access denied")})

		assert.False(t, w.confirmed(t.Context()))
	})

	t.Run("the probe targets the host the channel replicates from", func(t *testing.T) {
		conn := &fakeSourceConn{up: []bool{true}}
		w := newWatch(t, conn, &fakeDatabase{})

		require.True(t, w.confirmed(t.Context()))

		conn.mu.Lock()
		defer conn.mu.Unlock()
		for _, host := range conn.hosts {
			assert.Equal(t, "mysql-0.mysql", host)
		}
	})

	t.Run("a replica with no source host never probes", func(t *testing.T) {
		conn := &fakeSourceConn{up: []bool{true}}
		w := newSourceWatch(failoverConfig{
			newSourceDB:   conn.connect,
			sourcePoll:    time.Millisecond,
			sourceTimeout: time.Second,
		}, &fakeDatabase{}, "")

		assert.False(t, w.confirmed(t.Context()))

		dials, _ := conn.count()
		assert.Zero(t, dials)
	})

	t.Run("a cancelled job stops confirming", func(t *testing.T) {
		conn := &fakeSourceConn{up: []bool{true}}
		w := newSourceWatch(failoverConfig{
			newSourceDB:   conn.connect,
			sourcePoll:    time.Hour,
			sourceTimeout: time.Second,
		}, &fakeDatabase{}, "mysql-0.mysql")

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		assert.False(t, w.confirmed(ctx))
	})
}

func TestWatchSource(t *testing.T) {
	t.Run("closes the channel once the source is back", func(t *testing.T) {
		conn := &fakeSourceConn{up: []bool{false, false, true}}
		w := newWatch(t, conn, &fakeDatabase{})

		select {
		case <-watchSource(t.Context(), w):
		case <-time.After(10 * time.Second):
			t.Fatal("the watch never reported the source back")
		}
	})

	t.Run("a torn-down watch never reports the source back", func(t *testing.T) {
		conn := &fakeSourceConn{}
		w := newWatch(t, conn, &fakeDatabase{})

		ctx, cancel := context.WithCancel(t.Context())
		recovered := watchSource(ctx, w)
		cancel()

		time.Sleep(20 * time.Millisecond)

		select {
		case <-recovered:
			t.Fatal("a cancelled watch must not read as a recovered source")
		default:
		}
	})
}

func TestStandDown(t *testing.T) {
	t.Run("starts the receiver and reports the stand-down", func(t *testing.T) {
		f := &fakeDatabase{fakeStatuser: &fakeStatuser{
			statuses: []map[string]string{{"Replica_IO_Running": "Yes", "Source_Host": "mysql-0.mysql"}},
		}}

		err := newWatch(t, &fakeSourceConn{}, f).standDown(t.Context())

		require.ErrorIs(t, err, errSourceRecovered)
		assert.Equal(t, []string{"StartIOThread"}, f.ops, "the splice must be left alone")
	})

	t.Run("a receiver that never connects still stands down", func(t *testing.T) {
		f := &fakeDatabase{fakeStatuser: &fakeStatuser{
			statuses: []map[string]string{{"Replica_IO_Running": "Connecting"}},
		}}

		err := newWatch(t, &fakeSourceConn{}, f).standDown(t.Context())

		require.ErrorIs(t, err, errSourceRecovered,
			"the promotion has to be called off whether or not the receiver made it back")
	})

	t.Run("a receiver that will not start fails the job", func(t *testing.T) {
		f := &fakeDatabase{
			fakeStatuser: &fakeStatuser{statuses: []map[string]string{{}}},
			ioErr:        errors.New("access denied"),
		}

		err := newWatch(t, &fakeSourceConn{}, f).standDown(t.Context())

		require.Error(t, err)
		assert.NotErrorIs(t, err, errSourceRecovered)
		assert.Contains(t, err.Error(), "start IO_THREAD")
	})
}

func TestWaitForReceiver(t *testing.T) {
	t.Run("returns once the receiver is running", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{
			{"Replica_IO_Running": "Connecting"},
			{"Replica_IO_Running": "Yes"},
		}}

		waitForReceiver(t.Context(), f, time.Millisecond, time.Minute)

		assert.Equal(t, 2, f.calls)
	})

	t.Run("gives up without failing the stand-down", func(t *testing.T) {
		f := &fakeStatuser{statuses: []map[string]string{{"Replica_IO_Running": "Connecting"}}}

		waitForReceiver(t.Context(), f, time.Millisecond, 20*time.Millisecond)

		assert.Greater(t, f.calls, 1)
	})

	t.Run("an unreadable status does not block the exit", func(t *testing.T) {
		f := &fakeStatuser{err: errors.New("connection lost")}

		waitForReceiver(t.Context(), f, time.Millisecond, time.Minute)

		assert.Equal(t, 1, f.calls)
	})
}
