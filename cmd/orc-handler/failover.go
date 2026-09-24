package main

import (
	"bytes"
	"context"
	"flag"
	"strings"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	failoverlock "github.com/percona/percona-server-mysql-operator/cmd/internal/failover"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

const failoverBinary = "/opt/percona/failover"

const (
	// execSlack is the margin on top of everything the hook is known to wait for
	execSlack      = 10 * time.Second
	defaultTimeout = 6 * time.Hour

	// probeTimeout bounds the worker's look at the source before a forced promotion
	probeTimeout = time.Minute

	// sourcePodWait is how long the splice waits for a deleted primary's pod to be rescheduled
	sourcePodWait = 2 * time.Minute
	sourcePodPoll = 2 * time.Second
)

func runFailover(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("failover", flag.ExitOnError)
	source := fs.String("source", "", "Hostname of the server to fetch the missing binary logs from")
	target := fs.String("target", "", "Hostname of the server to apply the missing binary logs on. Defaults to the most up to date replica of the source")
	timeout := fs.Duration("timeout", defaultTimeout, "How long fetching and applying the missing binary logs may take, counted from the first attempt for this source")
	onTimeout := fs.String("on-timeout", string(apiv1.FailoverPolicyAbort), "What to do once the timeout expires: Abort or ForceWithPossibleDataLoss")
	failureType := fs.String("failure-type", "", "Analysis code orchestrator reported the problem as. The command only runs for the ones whose recovery promotes a replica")
	command := fs.String("command", "", "Orchestrator command that started the recovery. Empty for the failures orchestrator detects on its own")
	uid := fs.String("uid", "", "Orchestrator's identifier of the recovery running the hook")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if !orchestrator.IsMasterFailover(*failureType) {
		log.Info("Recovery does not promote a replica, nothing to apply", "failureType", *failureType)
		return nil
	}

	if orchestrator.IsPlannedTakeover(*command) {
		log.Info("Recovery is a planned takeover, nothing to apply", "command", *command)
		return nil
	}

	if *source == "" {
		return errors.New("source flag should not be empty")
	}

	if *timeout <= 0 {
		return errors.Errorf("timeout must be positive, got %s", *timeout)
	}

	policy := apiv1.FailoverPolicy(*onTimeout)
	if !policy.Valid() {
		return errors.Errorf("on-timeout must be %s or %s, got %q", apiv1.FailoverPolicyAbort, apiv1.FailoverPolicyForce, *onTimeout)
	}

	ctx, cancel := context.WithTimeout(ctx, hookTimeout(*timeout))
	defer cancel()

	g := newGate()

	err := guardedFailover(ctx, g, *source, *uid, func(ctx context.Context) error {
		remaining, err := budget(g, *source, *timeout)
		if err != nil {
			return err
		}

		if remaining <= 0 {
			err = timedOut(ctx, g, *source, *target, *timeout, policy)
		} else {
			err = failover(ctx, *source, *target, remaining)
		}

		switch {
		case isSourceRecovered(err):
			// The source is serving again, give the next failure the full budget.
			if clearErr := g.clearSeen(*source); clearErr != nil {
				log.Error(clearErr, "failed to reset the failover timeout", "source", *source)
			}

			// The cluster has its primary back, so there is nothing to warn about.
			if markErr := g.markNotified(*source); markErr != nil {
				log.Error(markErr, "failed to mute the failover report", "source", *source)
			}
		case err != nil:
			if markErr := g.refreshSeen(*source); markErr != nil {
				log.Error(markErr, "failed to refresh the failover timeout", "source", *source)
			}
		}

		return err
	})
	if err != nil {
		// Orchestrator abandons the recovery, and the block it holds on the
		// cluster is what keeps another one from starting until this recovery
		// is acknowledged
		ackRecovery(ctx, *uid)
	}

	return err
}

// ackWait bounds the retries of an acknowledgement
const (
	ackWait = time.Minute
	ackPoll = 5 * time.Second
)

// hookTimeout bounds one invocation of the hook. The worker may spend the
// whole budget, and the attempt still waits for the source pod before it and
// probes the source after it, then acknowledges the recovery on failure. The
// budget itself is enforced through what the worker is given, not through
// this deadline.
func hookTimeout(timeout time.Duration) time.Duration {
	return timeout + sourcePodWait + probeTimeout + ackWait + execSlack
}

// ackRecovery acknowledges the recovery running the hook, which lifts the
// block orchestrator holds on the cluster for RecoveryPeriodBlockSeconds
func ackRecovery(ctx context.Context, uid string) {
	if uid == "" {
		return
	}

	c, err := connect(ctx)
	if err != nil {
		log.Error(err, "failed to acknowledge the recovery", "uid", uid)
		return
	}

	deadline := time.Now().Add(ackWait)

	for {
		var orcPod *corev1.Pod
		orcPod, err = c.orchestratorPod(ctx)
		if err == nil {
			err = orchestrator.AckRecovery(ctx, c.cliCmd, orcPod, uid, "orc-handler: recovery finished")
		}
		if err == nil {
			log.Info("Acknowledged the recovery", "uid", uid)
			return
		}

		if !time.Now().Before(deadline) {
			log.Error(err, "failed to acknowledge the recovery", "uid", uid)
			return
		}

		log.Info("Failed to acknowledge the recovery, will retry", "uid", uid, "error", err.Error())

		select {
		case <-ctx.Done():
			return
		case <-time.After(ackPoll):
		}
	}
}

// budget returns what is left of the timeout for this source. It is measured
// from the first attempt rather than per attempt.
func budget(g *gate, source string, timeout time.Duration) (time.Duration, error) {
	start, err := g.markSeen(source)
	if err != nil {
		return 0, err
	}

	return timeout - time.Since(start), nil
}

// timedOut applies the configured policy once the whole budget is spent.
func timedOut(ctx context.Context, g *gate, source, target string, timeout time.Duration, policy apiv1.FailoverPolicy) error {
	if policy != apiv1.FailoverPolicyForce {
		log.Info("Timed out recovering the transactions stranded on the source, aborting the failover",
			"source", source, "timeout", timeout, "onTimeout", policy)

		if err := notify(ctx, g, source, naming.EventFailoverBlocked,
			"Could not recover the transactions stranded on %s within %s. The cluster stays without a"+
				" writable primary. Annotate the cluster with %s to promote once.",
			source, timeout, naming.AnnotationForcePromote); err != nil {
			log.Error(err, "failed to record the blocked failover event", "source", source)
		}

		return errors.Errorf("could not recover the transactions stranded on %s within %s", source, timeout)
	}

	c, orcPod, candidate, err := candidateFor(ctx, source, target)
	if err != nil {
		return err
	}
	if candidate == nil {
		log.Info("Source has no replicas, nothing to promote", "source", source)
		return nil
	}

	if err := probeSource(ctx, c, source, candidate.Hostname); err != nil {
		return err
	}

	if err := orchestrator.RegisterCandidate(ctx, c.cliCmd, orcPod, candidate.Hostname, candidate.Port, orchestrator.PromotionRulePrefer); err != nil {
		return errors.Wrapf(err, "register %s as promotion candidate", candidate.Hostname)
	}

	log.Info("Timed out recovering the transactions stranded on the source, promoting anyway",
		"source", source, "candidate", candidate.Hostname, "timeout", timeout)

	c.warn(ctx, naming.EventFailoverForced,
		"Could not recover the transactions stranded on %s within %s, promoting %s anyway."+
			" Transactions the old primary committed and never delivered are lost.",
		source, timeout, candidate.Hostname)

	return nil
}

// notify records an event unless one was already recorded for this source
// recently. Orchestrator retries a blocked recovery every few seconds and each
// retry runs this hook again.
func notify(ctx context.Context, g *gate, source, reason, format string, args ...any) error {
	should, err := g.notifyOnce(source)
	if err != nil || !should {
		return err
	}

	c, err := connect(ctx)
	if err != nil {
		return err
	}

	c.warn(ctx, reason, format, args...)

	return nil
}

func isSourceRecovered(err error) bool {
	return err != nil && strings.Contains(err.Error(), failoverlock.ResultSourceRecovered)
}

// guardedFailover runs the failover for one recovery at a time per source and
// refuses the ones that arrive while another is in flight. The claim it takes
// outlives the hook: the recovery's post hook releases it, so a recovery that
// is past its hook but not yet done promoting still holds the source.
func guardedFailover(ctx context.Context, g *gate, source, uid string, do func(context.Context) error) error {
	if err := g.claim(source, uid); err != nil {
		return err
	}

	stopKeeping := g.keepClaim(ctx, source)

	err := do(ctx)

	stopKeeping()

	if err != nil {
		// No post hook follows a failed pre hook, so the claim ends here.
		if _, releaseErr := g.release(source, uid); releaseErr != nil {
			log.Error(releaseErr, "failed to release the claim", "source", source)
		}

		return err
	}

	return nil
}

func failover(ctx context.Context, source, target string, timeout time.Duration) error {
	log := log.WithName("failover")

	c, orcPod, candidate, err := candidateFor(ctx, source, target)
	if err != nil {
		return err
	}
	if candidate == nil {
		log.Info("Source has no replicas, nothing to apply", "source", source)
		return nil
	}

	sourceIP, err := sourcePodIP(ctx, c, source, min(sourcePodWait, timeout))
	if err != nil {
		return err
	}

	log.Info("Applying missing binary logs", "candidate", candidate.Hostname, "source", source)

	if err := runWorker(ctx, c, sourceIP, candidate.Hostname, "-timeout", timeout.String()); err != nil {
		return err
	}

	log.Info("Applied missing binary logs", "candidate", candidate.Hostname, "source", source)

	if err := probeSource(ctx, c, source, candidate.Hostname); err != nil {
		return err
	}

	if err := orchestrator.RegisterCandidate(ctx, c.cliCmd, orcPod, candidate.Hostname, candidate.Port, orchestrator.PromotionRulePrefer); err != nil {
		return errors.Wrapf(err, "register %s as promotion candidate", candidate.Hostname)
	}

	log.Info("Registered the promotion candidate", "candidate", candidate.Hostname)
	return nil
}

// probeSource asks the worker on the candidate whether the source is back. A
// source whose pod is gone cannot be, so it is not an error.
func probeSource(ctx context.Context, c *cluster, source, candidate string) error {
	sourceIP, err := sourcePodIP(ctx, c, source, 0)
	if err == nil {
		err = runWorker(ctx, c, sourceIP, candidate, "-probe", "-timeout", probeTimeout.String())
	}
	if errors.Is(err, errNoSourcePod) {
		log.Info("Source pod is gone, nothing to probe", "source", source)
		return nil
	}

	return err
}

// sourcePodIP returns the address the worker reaches the source at, waiting up
// to wait for a pod that has not been scheduled yet. A zero wait looks once.
func sourcePodIP(ctx context.Context, c *cluster, source string, wait time.Duration) (string, error) {
	deadline := time.Now().Add(wait)

	for {
		pod, err := c.pod(ctx, source)
		switch {
		case err != nil && !apierrors.IsNotFound(err):
			return "", err
		case err == nil && pod.Status.PodIP != "":
			return pod.Status.PodIP, nil
		}

		if !time.Now().Before(deadline) {
			return "", errors.Wrapf(errNoSourcePod, "pod %s", podName(c.cr, source))
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(sourcePodPoll):
		}
	}
}

var errNoSourcePod = errors.New("the source pod is gone")

// runWorker runs the failover binary in the candidate's mysql container against
// the source's pod IP.
func runWorker(ctx context.Context, c *cluster, sourceIP, candidate string, args ...string) error {
	return execWorker(ctx, c, candidate, append([]string{"-source", sourceIP}, args...)...)
}

// execWorker runs the failover binary in the mysql container of host's pod. Its
// stdout ends up in the returned error, which is where isSourceRecovered looks
// for the worker's verdict.
func execWorker(ctx context.Context, c *cluster, host string, args ...string) error {
	pod, err := c.pod(ctx, host)
	if err != nil {
		return err
	}

	cmd := append([]string{failoverBinary}, args...)

	log.Info("Running the failover worker", "pod", pod.GetName(), "cmd", cmd)

	var stdout, stderr bytes.Buffer
	if err := c.cliCmd.Exec(ctx, pod, mysql.AppName, cmd, nil, &stdout, &stderr, false); err != nil {
		return errors.Wrapf(err, "run %s in pod %s, stdout: %s, stderr: %s", cmd, pod.GetName(), stdout.String(), stderr.String())
	}

	log.Info("Failover worker finished", "pod", pod.GetName(), "stdout", stdout.String(), "stderr", stderr.String())

	return nil
}

// candidateFor connects to the cluster and resolves the replica to promote,
// which is nil when the source has none.
func candidateFor(ctx context.Context, source, target string) (*cluster, *corev1.Pod, *orchestrator.InstanceKey, error) {
	c, err := connect(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	orcPod, err := orchestrator.GetReadyPod(ctx, c.client, c.cr)
	if err != nil {
		return nil, nil, nil, errors.Wrap(err, "get ready orchestrator pod")
	}

	candidate, err := pickCandidate(ctx, c, orcPod, source, target)
	if err != nil {
		return nil, nil, nil, err
	}

	return c, orcPod, candidate, nil
}

// pickCandidate returns the replica to promote, or nil when the source has
// none. An explicit target is taken as given.
func pickCandidate(ctx context.Context, c *cluster, orcPod *corev1.Pod, source, target string) (*orchestrator.InstanceKey, error) {
	if target != "" {
		return &orchestrator.InstanceKey{Hostname: target, Port: mysql.DefaultPort}, nil
	}

	instances, err := orchestrator.Cluster(ctx, c.cliCmd, orcPod, c.cr.ClusterHint())
	if err != nil {
		return nil, errors.Wrap(err, "get cluster instances")
	}

	replicas := orchestrator.ReplicasOf(instances, source)
	if len(replicas) == 0 {
		return nil, nil
	}

	candidate := orchestrator.MostUpToDate(replicas)
	log.Info("Picked the most up to date replica", "replica", candidate.Hostname, "replicas", len(replicas))

	return &candidate, nil
}
