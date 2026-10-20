package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	failoverlock "github.com/percona/percona-server-mysql-operator/cmd/internal/failover"
	perconaClientCmd "github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

const failoverBinary = "/opt/percona/failover"

const (
	// execSlack is the margin on top of everything the hook is known to wait for
	execSlack      = 10 * time.Second
	defaultTimeout = time.Hour

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
		c, err := connect(ctx)
		if err != nil {
			return err
		}
		if c.unprotected(ctx, g, *source) {
			return nil
		}

		remaining, started, err := budget(g, *source, *timeout)
		if err != nil {
			return err
		}

		if started {
			// The first attempt is the moment the cluster lost its primary, and
			// the wait that follows should not be a surprise at its end.
			if notifyErr := notify(ctx, g, *source, corev1.EventTypeNormal, naming.EventFailoverWaiting,
				"Recovering the transactions stranded on %s before a replica is promoted. The cluster has no"+
					" writable primary until that is done, for up to %s; then the %s policy applies.",
				*source, *timeout, policy); notifyErr != nil {
				log.Error(notifyErr, "failed to record the waiting failover event", "source", *source)
			}
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
			if markErr := g.markNotified(*source, naming.EventFailoverFailed); markErr != nil {
				log.Error(markErr, "failed to mute the failover report", "source", *source)
			}

			startReceivers(ctx, c, *source)
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

// unprotected reports whether the cluster runs without backups. The source's
// stream comes from the backup sidecar, so without it the stranded
// transactions cannot be recovered and Orchestrator promotes on its own.
func (c *cluster) unprotected(ctx context.Context, g *gate, source string) bool {
	if c.cr.BackupEnabled() {
		return false
	}

	log.Info("Backups are disabled, leaving the promotion to orchestrator", "source", source)

	should, err := g.notifyOnce(source, naming.EventFailoverUnprotected)
	if err != nil {
		log.Error(err, "failed to record the unprotected failover event", "source", source)
	}
	if should {
		c.warn(ctx, naming.EventFailoverUnprotected,
			"Backups are disabled, so the transactions stranded on %s cannot be recovered. Orchestrator"+
				" promotes a replica without them; anything the old primary committed and never delivered is lost.",
			source)
	}

	return true
}

// ackWait bounds the retries of an acknowledgement, and of the cluster
// connection the post hook needs before it
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

	err := retry(ctx, ackWait, func() error {
		c, orcPod, err := connectOrchestrator(ctx)
		if err != nil {
			return err
		}
		return orchestrator.AckRecovery(ctx, c.cliCmd, orcPod, uid, "orc-handler: recovery finished")
	})
	if err != nil {
		log.Error(err, "failed to acknowledge the recovery", "uid", uid)
		return
	}

	log.Info("Acknowledged the recovery", "uid", uid)
}

// connectOrchestrator connects to the cluster and picks the orchestrator pod
// to send API calls to
func connectOrchestrator(ctx context.Context) (*cluster, *corev1.Pod, error) {
	c, err := connect(ctx)
	if err != nil {
		return nil, nil, errors.Wrap(err, "connect to the cluster")
	}

	orcPod, err := c.orchestratorPod(ctx)
	if err != nil {
		return nil, nil, err
	}

	return c, orcPod, nil
}

// retry calls fn until it succeeds, wait has passed or ctx is done, and
// returns the last error
func retry(ctx context.Context, wait time.Duration, fn func() error) error {
	deadline := time.Now().Add(wait)

	for {
		err := fn()
		if err == nil || !time.Now().Before(deadline) {
			return err
		}

		log.Info("Retrying after a failure", "error", err.Error())

		select {
		case <-ctx.Done():
			return err
		case <-time.After(ackPoll):
		}
	}
}

// budget returns what is left of the timeout for this source, and whether
// this attempt is the one that started the clock. It is measured from the
// first attempt rather than per attempt.
func budget(g *gate, source string, timeout time.Duration) (time.Duration, bool, error) {
	start, started, err := g.markSeen(source)
	if err != nil {
		return 0, false, err
	}

	return timeout - time.Since(start), started, nil
}

// timedOut applies the configured policy once the whole budget is spent.
func timedOut(ctx context.Context, g *gate, source, target string, timeout time.Duration, policy apiv1.FailoverPolicy) error {
	if policy != apiv1.FailoverPolicyForce {
		log.Info("Timed out recovering the transactions stranded on the source, aborting the failover",
			"source", source, "timeout", timeout, "onTimeout", policy)

		if err := notify(ctx, g, source, corev1.EventTypeWarning, naming.EventFailoverBlocked,
			"Could not recover the transactions stranded on %s within %s. The cluster stays without a"+
				" writable primary. Annotate the cluster with %s to promote once.",
			source, timeout, naming.AnnotationForcePromote); err != nil {
			log.Error(err, "failed to record the blocked failover event", "source", source)
		}

		return errors.Errorf("could not recover the transactions stranded on %s within %s", source, timeout)
	}

	c, orcPod, candidate, others, err := candidateFor(ctx, source, target)
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

	// the policy accepts data loss, so orchestrator may fall back to another
	// replica if this one is gone by the time it promotes
	if err := registerCandidate(ctx, c.cliCmd, orcPod, *candidate, others, orchestrator.PromotionRuleNeutral); err != nil {
		return err
	}

	log.Info("Timed out recovering the transactions stranded on the source, promoting anyway",
		"source", source, "candidate", candidate.Hostname, "timeout", timeout)

	c.warn(ctx, naming.EventFailoverForced,
		"Could not recover the transactions stranded on %s within %s, promoting %s anyway."+
			" Transactions the old primary committed and never delivered are lost.",
		source, timeout, candidate.Hostname)

	return nil
}

// notify records an event unless one with the same reason was already
// recorded for this source recently. Orchestrator retries a blocked recovery
// every few seconds and each retry runs this hook again.
func notify(ctx context.Context, g *gate, source, eventType, reason, format string, args ...any) error {
	should, err := g.notifyOnce(source, reason)
	if err != nil || !should {
		return err
	}

	c, err := connect(ctx)
	if err != nil {
		return err
	}

	c.event(ctx, eventType, reason, format, args...)

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

	c, orcPod, candidate, others, err := candidateFor(ctx, source, target)
	if err != nil {
		return err
	}
	if candidate == nil {
		log.Info("Source has no replicas, nothing to apply", "source", source)
		return nil
	}

	if err := waitForSourcePod(ctx, c, source, min(sourcePodWait, timeout)); err != nil {
		return err
	}

	log.Info("Applying missing binary logs", "candidate", candidate.Hostname, "source", source)

	if err := runWorker(ctx, c, source, candidate.Hostname, "-timeout", timeout.String()); err != nil {
		return err
	}

	log.Info("Applied missing binary logs", "candidate", candidate.Hostname, "source", source)

	if err := probeSource(ctx, c, source, candidate.Hostname); err != nil {
		return err
	}

	if err := registerCandidate(ctx, c.cliCmd, orcPod, *candidate, others, orchestrator.PromotionRuleMustNot); err != nil {
		return err
	}

	log.Info("Registered the promotion candidate", "candidate", candidate.Hostname, "banned", len(others))
	return nil
}

// registerCandidate registers candidate as the replica to promote and the
// others under othersRule. The others go first: a candidate registered while
// one of them failed to be banned would leave orchestrator free to promote it
func registerCandidate(ctx context.Context, cliCmd perconaClientCmd.Client, orcPod *corev1.Pod, candidate orchestrator.InstanceKey, others []orchestrator.InstanceKey, othersRule string) error {
	for _, other := range others {
		if err := orchestrator.RegisterCandidate(ctx, cliCmd, orcPod, other.Hostname, other.Port, othersRule); err != nil {
			return errors.Wrapf(err, "register %s as %s", other.Hostname, othersRule)
		}
	}

	if err := orchestrator.RegisterCandidate(ctx, cliCmd, orcPod, candidate.Hostname, candidate.Port, orchestrator.PromotionRulePrefer); err != nil {
		return errors.Wrapf(err, "register %s as promotion candidate", candidate.Hostname)
	}

	return nil
}

// startReceivers starts the receivers that gave up on the source while it was down
func startReceivers(ctx context.Context, c *cluster, source string) {
	orcPod, err := orchestrator.GetReadyPod(ctx, c.client, c.cr)
	if err != nil {
		log.Error(err, "failed to start the receivers", "source", source)
		return
	}

	instances, err := orchestrator.Cluster(ctx, c.cliCmd, orcPod, fmt.Sprintf("%s:%d", source, mysql.DefaultPort))
	if err != nil {
		log.Error(err, "failed to start the receivers", "source", source)
		return
	}

	for _, replica := range orchestrator.ReplicasOf(instances, source) {
		if err := runWorker(ctx, c, source, replica.Key.Hostname, "-start-receiver"); err != nil {
			log.Error(err, "failed to start the receiver", "replica", replica.Key.Hostname, "source", source)
		}
	}
}

// probeSource asks the worker on the candidate whether the source is back. A
// source whose pod is gone cannot be, so it is not an error.
func probeSource(ctx context.Context, c *cluster, source, candidate string) error {
	err := waitForSourcePod(ctx, c, source, 0)
	if err == nil {
		err = runWorker(ctx, c, source, candidate, "-probe", "-timeout", probeTimeout.String())
	}
	if errors.Is(err, errNoSourcePod) {
		log.Info("Source pod is gone, nothing to probe", "source", source)
		return nil
	}

	return err
}

// waitForSourcePod waits up to wait for the source's pod to be scheduled. A zero
// wait looks once.
func waitForSourcePod(ctx context.Context, c *cluster, source string, wait time.Duration) error {
	deadline := time.Now().Add(wait)

	for {
		pod, err := c.pod(ctx, source)
		switch {
		case err != nil && !apierrors.IsNotFound(err):
			return err
		case err == nil && pod.Status.PodIP != "":
			return nil
		}

		if !time.Now().Before(deadline) {
			return errors.Wrapf(errNoSourcePod, "pod %s", podName(c.cr, source))
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sourcePodPoll):
		}
	}
}

var errNoSourcePod = errors.New("the source pod is gone")

// runWorker runs the failover binary in the candidate's mysql container against
// the source's hostname rather than its pod IP: a pod recreated while the worker
// waits for it comes back at another address.
func runWorker(ctx context.Context, c *cluster, source, candidate string, args ...string) error {
	return execWorker(ctx, c, candidate, append([]string{"-source", source}, args...)...)
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
// which is nil when the source has none, and the other replicas of the source
func candidateFor(ctx context.Context, source, target string) (*cluster, *corev1.Pod, *orchestrator.InstanceKey, []orchestrator.InstanceKey, error) {
	c, err := connect(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	orcPod, err := orchestrator.GetReadyPod(ctx, c.client, c.cr)
	if err != nil {
		return nil, nil, nil, nil, errors.Wrap(err, "get ready orchestrator pod")
	}

	candidate, others, err := pickCandidate(ctx, c, orcPod, source, target)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	return c, orcPod, candidate, others, nil
}

// pickCandidate returns the replica to promote, or nil when the source has
// none, and the other replicas of the source
func pickCandidate(ctx context.Context, c *cluster, orcPod *corev1.Pod, source, target string) (*orchestrator.InstanceKey, []orchestrator.InstanceKey, error) {
	// By the source rather than the alias, which a primary outside the
	// topology can hold.
	instances, err := orchestrator.Cluster(ctx, c.cliCmd, orcPod, fmt.Sprintf("%s:%d", source, mysql.DefaultPort))
	if err != nil {
		return nil, nil, errors.Wrap(err, "get cluster instances")
	}

	started := make(map[string]bool)
	for _, replica := range orchestrator.ReplicasOf(instances, source) {
		pod, err := c.pod(ctx, replica.Key.Hostname)
		if apierrors.IsNotFound(errors.Cause(err)) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}

		started[replica.Key.Hostname] = mysqlStarted(pod)
	}

	candidate, others, err := chooseCandidate(instances, source, target, started)
	if err != nil {
		return nil, nil, err
	}
	if candidate != nil {
		log.Info("Picked the replica to promote", "replica", candidate.Hostname, "others", len(others))
	}

	return candidate, others, nil
}

var errNoStartedReplica = errors.New("no replica of the source has started")

// chooseCandidate picks the most up to date replica of source among the started
// ones and returns the other replicas of source apart from it
func chooseCandidate(instances []*orchestrator.Instance, source, target string, started map[string]bool) (*orchestrator.InstanceKey, []orchestrator.InstanceKey, error) {
	replicas := orchestrator.ReplicasOf(instances, source)

	var candidate orchestrator.InstanceKey
	switch {
	case target != "":
		candidate = orchestrator.InstanceKey{Hostname: target, Port: mysql.DefaultPort}
	case len(replicas) > 0:
		eligible := make([]*orchestrator.Instance, 0, len(replicas))
		for _, replica := range replicas {
			if started[replica.Key.Hostname] {
				eligible = append(eligible, replica)
			}
		}
		if len(eligible) == 0 {
			return nil, nil, errors.Wrapf(errNoStartedReplica, "%d replica(s) of %s", len(replicas), source)
		}

		candidate = orchestrator.MostUpToDate(eligible)
	default:
		return nil, nil, nil
	}

	others := make([]orchestrator.InstanceKey, 0, len(replicas))
	for _, replica := range replicas {
		if replica.Key.Hostname != candidate.Hostname {
			others = append(others, replica.Key)
		}
	}

	return &candidate, others, nil
}

// mysqlStarted reports whether the pod's mysql container passed its startup
// probe since it last started
func mysqlStarted(pod *corev1.Pod) bool {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == mysql.AppName {
			return status.Started != nil && *status.Started
		}
	}

	return false
}
