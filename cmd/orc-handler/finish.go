package main

import (
	"context"
	"flag"

	"github.com/pkg/errors"
)

// runFinish closes a recovery on the hook's side once orchestrator is done
// with it, whichever way it ended. It runs from the post hooks, which
// orchestrator executes for every recovery that got past its pre hook.
func runFinish(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("finish", flag.ExitOnError)
	source := fs.String("source", "", "Hostname of the server the recovery was for")
	uid := fs.String("uid", "", "Orchestrator's identifier of the recovery")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *source == "" {
		return errors.New("source flag should not be empty")
	}

	finish(newGate(), *source, *uid)
	ackRecovery(ctx, *uid)

	return nil
}

// finish releases the claim uid holds on source
func finish(g *gate, source, uid string) {
	released, err := g.release(source, uid)
	if err != nil {
		log.Error(err, "failed to release the claim", "source", source, "uid", uid)
	}
	if !released {
		return
	}

	log.Info("Released the claim", "source", source, "uid", uid)

	// The recovery is over, so the next failure of this source starts its
	// timeout from scratch.
	if err := g.clearSeen(source); err != nil {
		log.Error(err, "failed to reset the failover timeout", "source", source)
	}
}
