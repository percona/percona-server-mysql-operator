package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

var errLocked = errors.New("another failover is in progress")

func lockSplice(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close() //nolint:errcheck

		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}

		return nil, fmt.Errorf("lock %s: %w", path, err)
	}

	return f, nil
}
