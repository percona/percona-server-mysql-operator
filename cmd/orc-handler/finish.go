package main

import (
	"context"
	"flag"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	perconaClientCmd "github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

// runFinish closes a recovery on the hook's side once orchestrator is done
// with it, whichever way it ended. It runs from the post hooks, which
// orchestrator executes for every recovery that got past its pre hook.
func runFinish(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("finish", flag.ExitOnError)
	source := fs.String("source", "", "Hostname of the server the recovery was for")
	uid := fs.String("uid", "", "Orchestrator's identifier of the recovery")
	failureType := fs.String("failure-type", "", "Analysis code of a recovery orchestrator gave up on without promoting. Only the post hook for unsuccessful recoveries passes it")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *source == "" {
		return errors.New("source flag should not be empty")
	}

	finish(newGate(), *source, *uid)

	// before the acknowledgement, which is what lets the next recovery start
	if c, err := connect(ctx); err != nil {
		log.Error(err, "failed to connect to the cluster")
	} else if orcPod, err := c.orchestratorPod(ctx); err != nil {
		log.Error(err, "failed to get the orchestrator pod")
	} else {
		clearCandidates(ctx, c.cliCmd, orcPod, c.cr)

		if err := endLostInRecovery(ctx, c.cliCmd, orcPod, *source, *failureType); err != nil {
			log.Error(err, "failed to end the lost-in-recovery downtime", "source", *source)
		}
	}

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

// clearCandidates lifts the promotion rules a failover left on the cluster's mysql hosts
func clearCandidates(ctx context.Context, cliCmd perconaClientCmd.Client, orcPod *corev1.Pod, cr *apiv1.PerconaServerMySQL) {
	for i := range int(cr.MySQLSpec().Size) {
		host := mysql.FQDN(cr, i)
		if err := orchestrator.RegisterCandidate(ctx, cliCmd, orcPod, host, mysql.DefaultPort, orchestrator.PromotionRuleNeutral); err != nil {
			log.Error(err, "failed to clear the promotion rule", "host", host)
		}
	}
}

// endLostInRecovery lets orchestrator retry a master recovery it gave up on
func endLostInRecovery(ctx context.Context, cliCmd perconaClientCmd.Client, orcPod *corev1.Pod, source, failureType string) error {
	if !orchestrator.IsMasterFailover(failureType) {
		return nil
	}

	instance, err := orchestrator.GetInstance(ctx, cliCmd, orcPod, source, mysql.DefaultPort)
	if err != nil {
		return errors.Wrapf(err, "get %s", source)
	}
	if !instance.IsDowntimed || instance.DowntimeReason != orchestrator.DowntimeReasonLostInRecovery {
		return nil
	}

	if err := orchestrator.EndDowntime(ctx, cliCmd, orcPod, source, mysql.DefaultPort); err != nil {
		return errors.Wrapf(err, "end the downtime of %s", source)
	}

	log.Info("Ended the lost-in-recovery downtime so orchestrator retries the recovery", "source", source)

	return nil
}
