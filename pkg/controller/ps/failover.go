package ps

import (
	"context"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

const (
	// forcePromoteAny is the annotation value that leaves the choice of which
	// replica to promote to the operator.
	forcePromoteAny = "true"

	reasonNoWritablePrimary = "NoWritablePrimary"
	reasonPrimaryWritable   = "PrimaryWritable"
)

// reconcileAsyncFailover keeps the cluster's failover state visible and serves
// the break-glass annotation.
func (r *PerconaServerMySQLReconciler) reconcileAsyncFailover(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	if !cr.Spec.MySQL.IsAsync() || !cr.OrchestratorEnabled() || cr.Spec.Orchestrator.Size <= 0 {
		return nil
	}

	_, forcePromote := cr.GetAnnotations()[naming.AnnotationForcePromote.String()]
	if cr.MySQLSpec().Size <= 1 && !forcePromote {
		return nil
	}

	orcPod, err := getReadyOrcPod(ctx, r.Client, cr)
	if err != nil {
		return nil
	}

	cluster, err := orchestrator.ResolveCluster(ctx, r.ClientCmd, orcPod, cr.ClusterHint())
	if err != nil {
		if forcePromote {
			return r.refuseForcePromote(ctx, cr, err)
		}

		return nil
	}

	r.reconcileStaleRecoveries(ctx, cr, orcPod)

	primary, err := orchestrator.ClusterPrimary(ctx, r.ClientCmd, orcPod, cluster)
	if err != nil {
		if forcePromote {
			return r.refuseForcePromote(ctx, cr, errors.Wrap(err, "orchestrator does not know the cluster's primary"))
		}

		return nil
	}

	if err := r.reconcileForcePromote(ctx, cr, orcPod, cluster, primary); err != nil {
		return err
	}

	return r.reconcileFailoverCondition(ctx, cr, primary)
}

// hasWritablePrimary reports whether the cluster can take writes right now.
func hasWritablePrimary(primary *orchestrator.Instance) bool {
	return !primary.ReadOnly && primary.IsLastCheckValid
}

func (r *PerconaServerMySQLReconciler) reconcileFailoverCondition(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	primary *orchestrator.Instance,
) error {
	if cr.MySQLSpec().Size <= 1 {
		return nil
	}

	if primary.Alias == "" {
		return nil
	}

	if primary.IsDowntimed && primary.DowntimeReason == orchestrator.DowntimeReasonSwitchover {
		return nil
	}

	condition := metav1.Condition{
		Type:               apiv1.ConditionAsyncFailoverBlocked,
		Status:             metav1.ConditionFalse,
		Reason:             reasonPrimaryWritable,
		Message:            "The cluster has a writable primary",
		ObservedGeneration: cr.Generation,
	}

	if !hasWritablePrimary(primary) {
		condition.Status = metav1.ConditionTrue
		condition.Reason = reasonNoWritablePrimary
		condition.Message = "The cluster has no writable primary. A failover may have aborted because the" +
			" transactions stranded on the old primary could not be recovered; see the cluster's events." +
			" Annotating the cluster with " + naming.AnnotationForcePromote.String() + " promotes a replica anyway."
	}

	existing := meta.FindStatusCondition(cr.Status.Conditions, apiv1.ConditionAsyncFailoverBlocked)
	if existing != nil && existing.Status == condition.Status && existing.Message == condition.Message {
		return nil
	}

	return writeStatus(ctx, r.Client, client.ObjectKeyFromObject(cr), func(status *apiv1.PerconaServerMySQLStatus) error {
		meta.SetStatusCondition(&status.Conditions, condition)
		return nil
	})
}

// reconcileForcePromote serves percona.com/force-promote-with-possible-data-loss.
func (r *PerconaServerMySQLReconciler) reconcileForcePromote(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	orcPod *corev1.Pod,
	cluster string,
	primary *orchestrator.Instance,
) error {
	value, ok := cr.GetAnnotations()[naming.AnnotationForcePromote.String()]
	if !ok {
		return nil
	}

	log := logf.FromContext(ctx).WithName("forcePromote")

	// A forced takeover neither fences nor re-points the old primary. With
	// one that still takes writes, the promoted replica misses them and the
	// old primary is left running on its own.
	if hasWritablePrimary(primary) {
		r.Recorder.Eventf(cr, corev1.EventTypeWarning, naming.EventFailoverForced,
			"Refusing to force a promotion: %s is a writable primary. Forcing one is only for a cluster"+
				" left without a writable primary.", primary.Alias)
		return r.consumeForcePromote(ctx, cr)
	}

	instances, err := orchestrator.Cluster(ctx, r.ClientCmd, orcPod, cluster)
	if err != nil {
		return errors.Wrap(err, "get cluster instances")
	}

	candidate, err := forcePromoteCandidate(instances, value)
	if err != nil {
		r.Recorder.Event(cr, corev1.EventTypeWarning, naming.EventFailoverForced, err.Error())
		return r.consumeForcePromote(ctx, cr)
	}

	if primary.Alias == candidate {
		log.Info("Requested candidate is already the primary, nothing to promote", "candidate", candidate)
		return r.consumeForcePromote(ctx, cr)
	}

	if err := orchestrator.RegisterCandidate(ctx, r.ClientCmd, orcPod, candidate, mysql.DefaultPort, orchestrator.PromotionRulePrefer); err != nil {
		return errors.Wrapf(err, "register %s as promotion candidate", candidate)
	}

	log.Info("Forcing promotion", "candidate", candidate)

	err = orchestrator.ForceMasterTakeover(ctx, r.ClientCmd, orcPod, cluster, candidate, mysql.DefaultPort)
	if errors.Is(err, orchestrator.ErrRecoveryNotAttempted) {
		// After an aborted failover orchestrator retries the dead primary's
		// recovery every second, and the takeover lost the race to one of them.
		return errors.Wrapf(err, "force the promotion of %s", candidate)
	}

	defer func() {
		if err := r.consumeForcePromote(ctx, cr); err != nil {
			log.Error(err, "failed to remove the annotation", "annotation", naming.AnnotationForcePromote)
		}
	}()

	if err != nil {
		r.Recorder.Eventf(cr, corev1.EventTypeWarning, naming.EventFailoverForced,
			"Could not force the promotion of %s: %v", candidate, err)
		return nil
	}

	r.Recorder.Eventf(cr, corev1.EventTypeWarning, naming.EventFailoverForced,
		"Forced the promotion of %s on request. Transactions the old primary committed and never"+
			" delivered are lost.", candidate)

	return nil
}

// refuseForcePromote answers the annotation when orchestrator cannot say which
// instance is the primary, or which cluster is this one. A forced takeover
// demotes the primary, so without one there is nothing orchestrator can do,
// and leaving the annotation in place would only hide that.
func (r *PerconaServerMySQLReconciler) refuseForcePromote(ctx context.Context, cr *apiv1.PerconaServerMySQL, cause error) error {
	r.Recorder.Eventf(cr, corev1.EventTypeWarning, naming.EventFailoverForced,
		"Could not force a promotion: %v", cause)

	return r.consumeForcePromote(ctx, cr)
}

func (r *PerconaServerMySQLReconciler) consumeForcePromote(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	return k8s.DeannotateObject(ctx, r.Client, cr, naming.AnnotationForcePromote)
}

// forcePromoteCandidate resolves the annotation's value to a pod that can be
// promoted. "true" leaves the choice to the operator, which ranks the replicas
// the same way the failover hook does.
func forcePromoteCandidate(instances []*orchestrator.Instance, value string) (string, error) {
	if value != forcePromoteAny {
		for _, instance := range instances {
			if instance.Alias == value {
				return value, nil
			}
		}

		return "", errors.Errorf("%s is not an instance of this cluster, refusing to promote it", value)
	}

	candidate, ok := orchestrator.BestCandidate(instances)
	if !ok {
		return "", errors.New("the cluster has no replica to promote")
	}

	return candidate.Hostname, nil
}

// staleRecoveryComment is what orchestrator's audit shows for the
// acknowledgements the operator makes.
const staleRecoveryComment = "percona-server-mysql-operator: recovery left active with nothing to end it"

// reconcileStaleRecoveries acknowledges the recoveries orchestrator still
// holds in their active period although nothing is left to end them. They are
// looked up by alias: a recovery keeps the cluster name it started with, which
// the promotion it made replaced.
func (r *PerconaServerMySQLReconciler) reconcileStaleRecoveries(ctx context.Context, cr *apiv1.PerconaServerMySQL, orcPod *corev1.Pod) {
	log := logf.FromContext(ctx).WithName("staleRecoveries")

	recs, err := orchestrator.UnacknowledgedRecoveries(ctx, r.ClientCmd, orcPod, cr.ClusterHint())
	if err != nil {
		log.V(1).Info("Could not read the unacknowledged recoveries", "error", err.Error())
		return
	}

	live, claimsKnown := r.liveRecoveryClaims(ctx, cr, recs)

	for _, rec := range staleRecoveries(recs, live, claimsKnown, time.Now()) {
		if err := orchestrator.AckRecovery(ctx, r.ClientCmd, orcPod, rec.UID, staleRecoveryComment); err != nil {
			log.Error(err, "failed to acknowledge the recovery", "uid", rec.UID, "failed", rec.Analysis.FailedKey.Hostname)
			continue
		}

		log.Info("Acknowledged a recovery orchestrator left active",
			"uid", rec.UID, "analysis", rec.Analysis.Analysis, "failed", rec.Analysis.FailedKey.Hostname, "ended", rec.Ended())
	}
}

// liveRecoveryClaims collects the recoveries whose pre-failover hook is still
// in flight on any orchestrator.
func (r *PerconaServerMySQLReconciler) liveRecoveryClaims(ctx context.Context, cr *apiv1.PerconaServerMySQL, recs []orchestrator.Recovery) (map[string]bool, bool) {
	live := make(map[string]bool)

	needed := false
	for _, rec := range recs {
		if rec.IsActive && !rec.Ended() {
			needed = true
			break
		}
	}
	if !needed {
		return live, false
	}

	pods, err := k8s.PodsByLabels(ctx, r.Client, orchestrator.MatchLabels(cr), cr.Namespace)
	if err != nil {
		return live, false
	}

	for i := range pods {
		// A hook dies with its orchestrator, so a pod that is not running holds
		// no claim that matters.
		if pods[i].Status.Phase != corev1.PodRunning {
			continue
		}

		uids, err := orchestrator.LiveClaims(ctx, r.ClientCmd, &pods[i])
		if err != nil {
			return live, false
		}
		for _, uid := range uids {
			live[uid] = true
		}
	}

	return live, true
}

// staleRecoveries picks the active recoveries nothing is left to end.
func staleRecoveries(recs []orchestrator.Recovery, live map[string]bool, claimsKnown bool, now time.Time) []orchestrator.Recovery {
	var stale []orchestrator.Recovery

	for _, rec := range recs {
		if !rec.IsActive {
			continue
		}

		if rec.Ended() {
			stale = append(stale, rec)
			continue
		}

		if !claimsKnown || live[rec.UID] {
			continue
		}

		started, ok := rec.Started()
		if !ok || now.Sub(started) < orchestrator.RecoveryClaimIdle {
			continue
		}

		stale = append(stale, rec)
	}

	return stale
}
