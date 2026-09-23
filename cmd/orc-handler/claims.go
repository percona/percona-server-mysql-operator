package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
)

// runClaims prints the uid of every recovery whose hook is still in flight on
// this orchestrator, one per line. The operator reads it before acknowledging a
// recovery orchestrator never closed: one that is not listed on any
// orchestrator has no hook left to finish it.
func runClaims(_ context.Context, _ []string) error {
	uids, err := newGate().liveClaims()
	if err != nil {
		return err
	}

	for _, uid := range uids {
		fmt.Println(uid)
	}

	return nil
}

// liveClaims returns the recoveries holding a claim that has not gone idle,
// which are the ones the gate would still keep other recoveries out for.
func (g *gate) liveClaims() ([]string, error) {
	dir := filepath.Join(g.dir, claimDir)

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.Wrapf(err, "read %s", dir)
	}

	uids := make([]string, 0, len(entries))
	for _, e := range entries {
		holder, age, ok, err := g.holder(e.Name())
		if err != nil {
			return nil, err
		}
		if ok && holder != "" && age < g.claimIdle {
			uids = append(uids, holder)
		}
	}

	return uids, nil
}
