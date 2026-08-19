package ps

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	database "github.com/percona/percona-server-mysql-operator/pkg/db"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// reconcileQuarantinedMembers resolves channel-less async members (those the
// bootstrap quarantined or left unjoined) per
// spec.mysql.errantTransactionsPolicy, and confirms the primary as writable
// after a full cluster restart. Such members are invisible to Orchestrator
// and excluded from traffic by HAProxy checks, so everything runs via pod
// exec.
func (r *PerconaServerMySQLReconciler) reconcileQuarantinedMembers(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	primary *orchestrator.Instance,
) error {
	log := logf.FromContext(ctx).WithName("reconcileQuarantinedMembers")

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr), cr.Namespace)
	if err != nil {
		return errors.Wrap(err, "get mysql pods")
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}
	replicaPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1.UserReplication)
	if err != nil {
		return errors.Wrap(err, "get replication password")
	}

	var primaryPod *corev1.Pod
	writableExists := false
	for i := range pods {
		pod := &pods[i]
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		um := database.NewReplicationManager(pod, r.ClientCmd, apiv1.UserOperator, operatorPass, "127.0.0.1")
		readOnly, err := um.IsReadonly(ctx)
		if err != nil {
			continue
		}
		if !readOnly {
			writableExists = true
		}
		if mysql.PodFQDN(cr, pod) == primary.Key.Hostname {
			primaryPod = pod
		}
	}
	if primaryPod == nil {
		return nil
	}
	pm := database.NewReplicationManager(primaryPod, r.ClientCmd, apiv1.UserOperator, operatorPass, "127.0.0.1")

	// Confirm the primary after a full cluster restart: making it writable
	// unblocks the bootstrap of the remaining members.
	if !writableExists {
		status, _, err := pm.ReplicationStatus(ctx)
		if err != nil {
			return errors.Wrap(err, "get primary replication status")
		}
		quarantined, err := isQuarantined(ctx, r.ClientCmd, primaryPod)
		if err != nil {
			return errors.Wrap(err, "check primary quarantine state")
		}
		if status == database.ReplicationStatusNotInitiated && !quarantined {
			log.Info("No writable member; confirming Orchestrator's primary as writable", "pod", primaryPod.Name)
			if err := pm.SetWritable(ctx); err != nil {
				return errors.Wrapf(err, "set %s writable", primaryPod.Name)
			}
			r.Recorder.Eventf(cr, corev1.EventTypeNormal, "PrimaryConfirmed",
				"member %s confirmed as the writable primary after cluster restart", primaryPod.Name)

			// The operator took over the promotion, so Orchestrator won't run the
			// failover hook (orc-handler) that labels the primary. Label it here so
			// the async primary still carries mysql.percona.com/primary=true.
			if err := r.reconcileAsyncPrimaryLabel(ctx, pods, primaryPod); err != nil {
				return errors.Wrap(err, "reconcile async primary label")
			}
		}
	}

	for i := range pods {
		pod := &pods[i]
		if mysql.PodFQDN(cr, pod) == primary.Key.Hostname || pod.Status.Phase != corev1.PodRunning {
			continue
		}

		um := database.NewReplicationManager(pod, r.ClientCmd, apiv1.UserOperator, operatorPass, "127.0.0.1")

		status, _, err := um.ReplicationStatus(ctx)
		if err != nil {
			log.V(1).Info("Can't check replication status, skipping", "pod", pod.Name, "error", err.Error())
			continue
		}
		// Gate on the marker, not the status: a quarantined former replica can be
		// Stopped (kept its channel), while a backup-stopped replica is left alone.
		quarantined, err := isQuarantined(ctx, r.ClientCmd, pod)
		if err != nil {
			log.V(1).Info("Can't check quarantine state, skipping", "pod", pod.Name, "error", err.Error())
			continue
		}
		if !quarantined && status != database.ReplicationStatusNotInitiated {
			continue
		}

		// Recompute divergence instead of trusting the quarantine marker.
		memberGTIDs, err := um.GetGTIDExecuted(ctx)
		if err != nil {
			return errors.Wrapf(err, "get gtid_executed of %s", pod.Name)
		}
		primaryGTIDs, err := pm.GetGTIDExecuted(ctx)
		if err != nil {
			return errors.Wrap(err, "get gtid_executed of primary")
		}
		errant, err := um.GTIDSubtract(ctx, memberGTIDs, primaryGTIDs)
		if err != nil {
			return errors.Wrapf(err, "compute errant GTIDs of %s", pod.Name)
		}

		if errant == "" {
			log.Info("Quarantined member no longer diverges, joining it to the cluster", "pod", pod.Name)
			if err := r.joinQuarantinedMember(ctx, cr, pod, um, primary, replicaPass); err != nil {
				return err
			}
			continue
		}

		switch cr.Spec.MySQL.ErrantTransactionsPolicy {
		case apiv1.ErrantTransactionsRebuild:
			log.Info("Quarantined member has errant GTIDs, policy is 'rebuild': deleting member pod and PVC to re-provision from primary",
				"pod", pod.Name, "errantGTIDs", errant)
			r.Recorder.Eventf(cr, corev1.EventTypeWarning, "ErrantMemberRebuild",
				"quarantined member %s has transactions not present on primary %s (errant GTIDs: %s); rebuilding the member, unreplicated data will be lost",
				pod.Name, primary.Alias, errant)

			pvcName := fmt.Sprintf("%s-%s", mysql.DataVolumeName, pod.Name)
			if err := r.deleteDataPVC(ctx, cr.Namespace, pvcName); err != nil {
				return errors.Wrapf(err, "delete PVC %s", pvcName)
			}
			if err := r.Delete(ctx, pod, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}}); client.IgnoreNotFound(err) != nil {
				return errors.Wrapf(err, "delete pod %s", pod.Name)
			}

		case apiv1.ErrantTransactionsInjectEmpty:
			log.Info("Quarantined member has errant GTIDs, policy is 'inject-empty': injecting empty transactions on primary and joining the member",
				"pod", pod.Name, "errantGTIDs", errant)
			r.Recorder.Eventf(cr, corev1.EventTypeWarning, "ErrantGTIDsInjectEmpty",
				"quarantined member %s has transactions not present on primary %s (errant GTIDs: %s); injecting empty transactions, diverged rows remain on the member",
				pod.Name, primary.Alias, errant)

			if err := pm.InjectEmptyGTIDs(ctx, errant); err != nil {
				return errors.Wrapf(err, "inject empty GTIDs on primary for %s", pod.Name)
			}
			if err := r.joinQuarantinedMember(ctx, cr, pod, um, primary, replicaPass); err != nil {
				return err
			}

		default: // apiv1.ErrantTransactionsManual
			log.Info("Quarantined member has errant GTIDs; not joining it (policy: manual). "+
				"Inspect the unreplicated transactions on the member, then rebuild it or set spec.mysql.errantTransactionsPolicy",
				"pod", pod.Name, "errantGTIDs", errant)
			r.Recorder.Eventf(cr, corev1.EventTypeWarning, "ErrantGTIDsDetected",
				"member %s is quarantined: it has transactions not present on primary %s (errant GTIDs: %s); not joined to the cluster (policy: manual)",
				pod.Name, primary.Alias, errant)
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) joinQuarantinedMember(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	pod *corev1.Pod,
	um *database.ReplicationDBManager,
	primary *orchestrator.Instance,
	replicaPass string,
) error {
	log := logf.FromContext(ctx).WithName("joinQuarantinedMember")

	sourceRetryCount, sourceConnectRetry, err := asyncSourceEnvValues(cr)
	if err != nil {
		return errors.Wrap(err, "failed to parse env vars")
	}

	if err := um.ChangeReplicationSource(ctx, primary.Key.Hostname, replicaPass, primary.Key.Port, sourceRetryCount, sourceConnectRetry); err != nil {
		return errors.Wrapf(err, "change replication source on %s", pod.Name)
	}
	if err := um.StartReplication(ctx); err != nil {
		return errors.Wrapf(err, "start replication on %s", pod.Name)
	}
	if err := removeQuarantineFile(ctx, r.ClientCmd, pod); err != nil {
		return errors.Wrapf(err, "remove quarantine file on %s", pod.Name)
	}

	log.Info("Member joined the cluster", "pod", pod.Name, "primary", primary.Key.Hostname)
	r.Recorder.Eventf(cr, corev1.EventTypeNormal, "MemberJoined",
		"member %s joined the cluster as a replica of %s", pod.Name, primary.Alias)

	return nil
}

func isQuarantined(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod) (bool, error) {
	var outb, errb bytes.Buffer
	err := cliCmd.Exec(ctx, pod, mysql.AppName,
		[]string{"sh", "-c", "test -f " + mysql.QuarantineFile + " && echo yes || echo no"}, nil, &outb, &errb, false)
	if err != nil {
		return false, errors.Wrapf(err, "stdout: %s, stderr: %s", outb.String(), errb.String())
	}
	return strings.TrimSpace(outb.String()) == "yes", nil
}

func removeQuarantineFile(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod) error {
	var outb, errb bytes.Buffer
	err := cliCmd.Exec(ctx, pod, mysql.AppName,
		[]string{"rm", "-f", mysql.QuarantineFile}, nil, &outb, &errb, false)
	return errors.Wrapf(err, "stdout: %s, stderr: %s", outb.String(), errb.String())
}

// reconcileAsyncPrimaryLabel puts mysql.percona.com/primary=true on primaryPod
// and clears it from the others. On async clusters this label is normally set by
// Orchestrator's failover hook (orc-handler); when the operator confirms the
// primary itself that hook never runs, so nothing else would label it.
func (r *PerconaServerMySQLReconciler) reconcileAsyncPrimaryLabel(ctx context.Context, pods []corev1.Pod, primaryPod *corev1.Pod) error {
	for i := range pods {
		pod := &pods[i]
		_, hasLabel := pod.Labels[naming.LabelMySQLPrimary]
		switch {
		case pod.Name == primaryPod.Name && !hasLabel:
			if err := r.assignPrimaryLabel(ctx, pod); err != nil {
				return errors.Wrapf(err, "assign primary label to %s", pod.Name)
			}
		case pod.Name != primaryPod.Name && hasLabel:
			if err := r.removePrimaryLabel(ctx, pod); err != nil {
				return errors.Wrapf(err, "remove primary label from %s", pod.Name)
			}
		}
	}
	return nil
}

// deleteDataPVC fetches the member's data PVC and deletes it with a UID
// precondition, so a same-named PVC the StatefulSet recreated while
// reprovisioning is never deleted (which would loop the rebuild and lose data).
func (r *PerconaServerMySQLReconciler) deleteDataPVC(ctx context.Context, namespace, name string) error {
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, pvc); err != nil {
		return client.IgnoreNotFound(err)
	}
	err := r.Delete(ctx, pvc, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pvc.UID}})
	return client.IgnoreNotFound(err)
}

// repairBrokenReplicas re-issues CHANGE REPLICATION SOURCE on replicas whose
// IO thread exists but is not running: after a takeover Orchestrator creates
// the demoted primary's channel without SOURCE_SSL/GET_SOURCE_PUBLIC_KEY, so
// caching_sha2_password auth fails and never recovers on its own.
func (r *PerconaServerMySQLReconciler) repairBrokenReplicas(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	orcPod *corev1.Pod,
	primary *orchestrator.Instance,
	clusterInstances []*orchestrator.Instance,
) error {
	log := logf.FromContext(ctx).WithName("repairBrokenReplicas")

	broken := make([]*orchestrator.Instance, 0)
	for _, instance := range clusterInstances {
		if instance.Alias == primary.Alias {
			continue
		}
		if instance.MasterKey.Hostname != primary.Key.Hostname {
			continue
		}

		// Errant GTIDs need the policy treatment even with running threads:
		// MySQL replicates past errant GTIDs silently. IsLastCheckValid
		// guards against acting on stale Orchestrator data right after a
		// rebuild, which would delete the new pod mid-clone.
		if instance.GtidErrant != "" && instance.IsLastCheckValid {
			if err := r.handleErrantTransactions(ctx, cr, orcPod, primary, instance); err != nil {
				return errors.Wrapf(err, "handle errant transactions on %s", instance.Alias)
			}
			continue
		}

		// Heal only a replica whose IO thread is stuck connecting/erroring (Other);
		// a cleanly Stopped IO thread is an intentional STOP REPLICA (user, backup,
		// or test) and must be left alone.
		if instance.ReplicationIOThreadState == orchestrator.ReplicationThreadStateOther {
			broken = append(broken, instance)
		}
	}
	if len(broken) == 0 {
		return nil
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}
	replicaPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1.UserReplication)
	if err != nil {
		return errors.Wrap(err, "get replication password")
	}
	sourceRetryCount, sourceConnectRetry, err := asyncSourceEnvValues(cr)
	if err != nil {
		return errors.Wrap(err, "failed to parse env vars")
	}

	for _, instance := range broken {
		log.Info("Replica IO thread is not running, reconfiguring replication",
			"replica", instance.Alias, "primary", primary.Key.Hostname, "ioThreadState", instance.ReplicationIOThreadState)

		idx, err := getPodIndexFromHostname(instance.Key.Hostname)
		if err != nil {
			return err
		}
		mysqlPod, err := mysql.GetPod(ctx, r.Client, cr, idx)
		if err != nil {
			return err
		}

		// Loopback exec: a broken replica is unready and has no DNS record.
		um := database.NewReplicationManager(mysqlPod, r.ClientCmd, apiv1.UserOperator, operatorPass, "127.0.0.1")
		if err := um.StopReplication(ctx); err != nil {
			return errors.Wrapf(err, "stop replication on %s", instance.Alias)
		}

		if err := um.ChangeReplicationSource(ctx, primary.Key.Hostname, replicaPass, primary.Key.Port, sourceRetryCount, sourceConnectRetry); err != nil {
			return errors.Wrapf(err, "change replication source on %s", instance.Alias)
		}

		if err := um.StartReplication(ctx); err != nil {
			return errors.Wrapf(err, "start replication on %s", instance.Alias)
		}

		log.Info("Reconfigured replication on replica", "replica", instance.Alias)
	}

	return nil
}

// handleErrantTransactions applies spec.mysql.errantTransactionsPolicy to an
// instance that holds transactions never replicated to the current primary.
func (r *PerconaServerMySQLReconciler) handleErrantTransactions(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	orcPod *corev1.Pod,
	primary *orchestrator.Instance,
	instance *orchestrator.Instance,
) error {
	log := logf.FromContext(ctx).WithName("handleErrantTransactions").
		WithValues("replica", instance.Alias, "errantGTIDs", instance.GtidErrant)

	switch cr.Spec.MySQL.ErrantTransactionsPolicy {
	case apiv1.ErrantTransactionsRebuild:
		log.Info("Errant GTIDs detected, policy is 'rebuild': deleting member pod and PVC to re-provision from primary")
		r.Recorder.Eventf(cr, corev1.EventTypeWarning, "ErrantMemberRebuild",
			"replica %s has transactions not present on primary %s (errant GTIDs: %s); rebuilding the member, unreplicated data will be lost",
			instance.Alias, primary.Alias, instance.GtidErrant)

		idx, err := getPodIndexFromHostname(instance.Key.Hostname)
		if err != nil {
			return err
		}
		mysqlPod, err := mysql.GetPod(ctx, r.Client, cr, idx)
		if err != nil {
			return errors.Wrap(err, "get mysql pod")
		}

		pvcName := fmt.Sprintf("%s-%s", mysql.DataVolumeName, mysqlPod.Name)
		if err := r.deleteDataPVC(ctx, cr.Namespace, pvcName); err != nil {
			return errors.Wrapf(err, "delete PVC %s", pvcName)
		}
		if err := r.Delete(ctx, mysqlPod, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &mysqlPod.UID}}); client.IgnoreNotFound(err) != nil {
			return errors.Wrapf(err, "delete pod %s", mysqlPod.Name)
		}

	case apiv1.ErrantTransactionsInjectEmpty:
		log.Info("Errant GTIDs detected, policy is 'inject-empty': reconciling GTID sets via Orchestrator")
		r.Recorder.Eventf(cr, corev1.EventTypeWarning, "ErrantGTIDsInjectEmpty",
			"replica %s has transactions not present on primary %s (errant GTIDs: %s); injecting empty transactions, diverged rows remain on the replica",
			instance.Alias, primary.Alias, instance.GtidErrant)

		if err := orchestrator.InjectEmptyGTIDs(ctx, r.ClientCmd, orcPod, instance.Key.Hostname, instance.Key.Port); err != nil {
			return errors.Wrapf(err, "inject empty GTIDs for %s", instance.Alias)
		}

		// Resume a stopped channel right away: an unready member has no DNS
		// record, so no later reconcile could reach it through Orchestrator.
		if !instance.ReplicationIOThreadState.IsRunning() || !instance.ReplicationSQLThreadState.IsRunning() {
			operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1.UserOperator)
			if err != nil {
				return errors.Wrap(err, "get operator password")
			}
			idx, err := getPodIndexFromHostname(instance.Key.Hostname)
			if err != nil {
				return err
			}
			mysqlPod, err := mysql.GetPod(ctx, r.Client, cr, idx)
			if err != nil {
				return errors.Wrap(err, "get mysql pod")
			}
			// Loopback: the unready pod has no headless-service DNS record.
			um := database.NewReplicationManager(mysqlPod, r.ClientCmd, apiv1.UserOperator, operatorPass, "127.0.0.1")
			if err := um.StartReplication(ctx); err != nil {
				return errors.Wrapf(err, "start replication on %s", instance.Alias)
			}
			log.Info("Restarted replication after injecting empty GTIDs", "replica", instance.Alias)
		}

	default: // apiv1.ErrantTransactionsManual
		log.Info("Replica has errant GTIDs, skipping automatic replication repair; " +
			"inspect the unreplicated transactions and resolve via Orchestrator " +
			"(gtid-errant-inject-empty or gtid-errant-reset-master), rebuild the member, " +
			"or set spec.mysql.errantTransactionsPolicy")
		r.Recorder.Eventf(cr, corev1.EventTypeWarning, "ErrantGTIDsDetected",
			"replica %s has transactions not present on primary %s (errant GTIDs: %s); automatic repair skipped (policy: manual)",
			instance.Alias, primary.Alias, instance.GtidErrant)
	}

	return nil
}
