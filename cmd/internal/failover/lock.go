package failover

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

const LockPath = "/var/lib/mysql/failover.lock"

var ErrLocked = errors.New("another failover is in progress")

func Lock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close() //nolint:errcheck

		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}

		return nil, fmt.Errorf("lock %s: %w", path, err)
	}

	return f, nil
}

// LockWait takes the lock at path like Lock does, waiting up to wait for the
// holder to release it. It returns ErrLocked once the wait is spent.
//
// The wait polls instead of blocking in flock so that it can be cancelled.
func LockWait(ctx context.Context, path string, wait, poll time.Duration) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}

	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close() //nolint:errcheck
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}

		select {
		case <-ctx.Done():
			f.Close() //nolint:errcheck
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, ErrLocked
			}

			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// InProgress reports whether a failover job currently holds the lock at path.
// The probe takes a shared lock, which is enough to conflict with the exclusive
// one Lock holds, and releases it again on return.
func InProgress(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}

		return false, fmt.Errorf("open lock file %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}

		return false, fmt.Errorf("lock %s: %w", path, err)
	}

	return false, nil
}
