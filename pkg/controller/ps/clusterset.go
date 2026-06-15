package ps

import (
	"bytes"
	"context"
	"strings"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clusterset"
	database "github.com/percona/percona-server-mysql-operator/pkg/db"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// reconcileClusterSetStatus adds a status condition when the cluster is replica member of a ClusterSet.
// When a replica member is removed from the ClusterSet, this function ensures that GR is bootstrapped again and the cluster is able to accept writes.
func (r *PerconaServerMySQLReconciler) reconcileClusterSetStatus(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx)
	if cr.Spec.MySQL.ClusterType != apiv1.ClusterTypeGR {
		return nil
	}

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr), cr.Namespace)
	if err != nil {
		return errors.Wrap(err, "get pods")
	}

	// Setting the condition only requires the first pod to be queryable. Waiting
	// for the full cluster would delay it (and everything derived from it, like
	// the HAProxy is_clusterset_replica flag) until the last member finishes cloning.
	if cr.Spec.Pause || len(pods) == 0 {
		return nil
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	// Check for clusterset_replication channel running on any of the nodes.
	replicationRunning, err := r.isClusterSetReplicationRunning(ctx, cr, operatorPass, pods)
	if err != nil {
		return errors.Wrap(err, "check if cluster set replication is running")
	}

	if replicationRunning {
		if !meta.IsStatusConditionTrue(cr.Status.Conditions, apiv1.ConditionClusterSetReplicationRunning) {
			if err := writeStatus(ctx, r.Client, client.ObjectKeyFromObject(cr), func(status *apiv1.PerconaServerMySQLStatus) error {
				meta.SetStatusCondition(&status.Conditions, metav1.Condition{
					Type:    apiv1.ConditionClusterSetReplicationRunning,
					Status:  metav1.ConditionTrue,
					Reason:  "ClusterSetReplicationRunning",
					Message: "ClusterSet replication is running",
				})
				return nil
			}); err != nil {
				return errors.Wrap(err, "write status condition")
			}
		}
		return nil
	}

	// Channel does not exist and it did not exist before either, nothing to do.
	if cond := meta.FindStatusCondition(cr.Status.Conditions, apiv1.ConditionClusterSetReplicationRunning); cond == nil {
		return nil
	}

	// At this point we know that there was a channel running earlier, but not anymore.
	// One of two things may have happened: (1) cluster was removed from clusterset, or (2) cluster was promoted to primary.
	//
	// This branch makes destructive decisions (lifting read-only, deleting pods), and its
	// inputs (cluster role, member states) have transient look-alike states while pods are
	// restarting or recovering. Only evaluate it once the cluster is settled: full size and
	// every pod ready.
	if len(pods) < int(cr.MySQLSpec().Size) {
		return nil
	}
	for _, pod := range pods {
		if !k8s.IsPodReady(pod) {
			return nil
		}
	}

	isClusterPrimary, err := r.isClusterRolePrimary(ctx, cr, operatorPass, pods[0])
	if err != nil {
		return errors.Wrap(err, "check if cluster is clusterset primary")
	}

	db := database.NewReplicationManager(&pods[0], r.ClientCmd, apiv1.UserOperator, operatorPass, mysql.ServiceName(cr))
	members, err := db.GetGroupReplicationMembers(ctx)
	if err != nil {
		return errors.Wrap(err, "get group replication members")
	}

	clusterDissolved := func() bool {
		return len(members) == 0 || (len(members) == 1 && members[0].MemberState == innodbcluster.MemberStateOffline)
	}

	if !isClusterPrimary {
		// Wait for mysqlshell to completely dissolve the cluster.
		if !clusterDissolved() {
			return nil
		}

		log.Info("Recovering former clusterset member cluster")
		if err := r.recoverClustersetReplicaCluster(ctx, cr, operatorPass, pods); err != nil {
			return errors.Wrap(err, "recover clusterset replica cluster")
		}
	}

	if err := writeStatus(ctx, r.Client, client.ObjectKeyFromObject(cr), func(status *apiv1.PerconaServerMySQLStatus) error {
		meta.RemoveStatusCondition(&status.Conditions, apiv1.ConditionClusterSetReplicationRunning)
		return nil
	}); err != nil {
		return errors.Wrap(err, "write status condition")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) isClusterRolePrimary(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	operatorPass string,
	pod corev1.Pod,
) (bool, error) {
	podFQDN := mysql.PodFQDN(cr, &pod)
	podUri := mysqlsh.URI(string(apiv1.UserOperator), operatorPass, podFQDN)

	opts := &mysqlsh.ExecOptions{
		Pod:           &pod,
		ContainerName: "mysql",
		Client:        r.ClientCmd,
		Stdout:        &bytes.Buffer{},
	}

	shell, err := mysqlsh.NewWithExec(podUri, opts)
	if err != nil {
		return false, errors.Wrap(err, "new mysqlsh")
	}

	status, err := shell.ClusterStatusWithExec(ctx)
	if err != nil {
		// GR dissolution has already started
		if strings.Contains(err.Error(), "not available through a session to a standalone instance") {
			return false, nil
		}
		return false, errors.Wrap(err, "get cluster status")
	}

	return status.ClusterRole == clusterset.ClusterRolePrimary, nil
}

// Why is this needed: Once a replica is removed from the ClusterSet, mysqlshell always dissolves GR, removes related metadata
// and leaves the cluster in a read-only state. This routine ensures that the cluster is able to start again as a GR cluster and accept new writes.
// This is automated in mysqlshell 9.7 (which is not supported at the time of writing this), but that will require investigation as well.
// For more details, see: https://dev.mysql.com/doc/mysql-shell/8.4/en/innodb-clusterset-remove.html
func (r *PerconaServerMySQLReconciler) recoverClustersetReplicaCluster(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	operatorPass string,
	pods []corev1.Pod,
) error {
	// Remove read_only and super_read_only
	admin := database.NewAdminManager(&pods[0], r.ClientCmd, apiv1.UserOperator, operatorPass, mysql.ServiceName(cr))
	if err := admin.SetReadOnly(ctx, false); err != nil {
		return errors.Wrap(err, "set read_only")
	}
	if err := admin.SetSuperReadOnly(ctx, false); err != nil {
		return errors.Wrap(err, "set super_read_only")
	}

	// Prepare pods for recovery
	var outb, errb bytes.Buffer
	cmd := []string{"touch", naming.ClusterSetRecoveryFile}
	for _, pod := range pods {
		outb.Reset()
		errb.Reset()
		err := r.ClientCmd.Exec(ctx, &pod, "mysql", cmd, nil, &outb, &errb, false)
		if err != nil {
			return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, outb.String(), errb.String())
		}
	}

	// Patch the .spec.mysql.bootstrap.mode to apiv1.BootstrapModeAuto
	orig := cr.DeepCopy()
	cr.Spec.MySQL.Bootstrap.Mode = new(apiv1.BootstrapModeAuto)
	if err := r.Patch(ctx, cr, client.MergeFrom(orig)); err != nil {
		return errors.Wrapf(err, "patch cr")
	}

	// Scale down the statefulset to 0 replicas
	// So that pods do not come up until the bootstrap mode has taken effect into the StatefulSet
	sfs := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mysql.Name(cr),
			Namespace: cr.Namespace,
		},
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sfs), sfs); err != nil {
		return errors.Wrapf(err, "get statefulset")
	}
	origSfs := sfs.DeepCopy()
	sfs.Spec.Replicas = new(int32(0))
	delete(sfs.Annotations, naming.AnnotationLastConfigHash.String())
	if err := r.Patch(ctx, sfs, client.MergeFrom(origSfs)); err != nil {
		return errors.Wrapf(err, "patch statefulset")
	}

	// Delete all the pods, when they come up, GR bootstrap starts again with the new bootstrap mode
	if err := r.DeleteAllOf(ctx, &corev1.Pod{}, &client.DeleteAllOfOptions{
		ListOptions: client.ListOptions{
			LabelSelector: labels.SelectorFromSet(mysql.MatchLabels(cr)),
			Namespace:     cr.Namespace,
		},
	}); err != nil {
		return errors.Wrapf(err, "delete mysql pods")
	}

	if err := writeStatus(ctx, r.Client, client.ObjectKeyFromObject(cr), func(status *apiv1.PerconaServerMySQLStatus) error {
		meta.RemoveStatusCondition(&status.Conditions, apiv1.ConditionInnoDBClusterBootstrapped)
		return nil
	}); err != nil {
		return errors.Wrap(err, "write status condition")
	}
	return nil
}

func (r *PerconaServerMySQLReconciler) isClusterSetReplicationRunning(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	operatorPass string,
	pods []corev1.Pod,
) (bool, error) {
	for _, pod := range pods {
		if !k8s.IsPodReady(pod) {
			continue
		}
		db := database.NewReplicationManager(&pod, r.ClientCmd, apiv1.UserOperator, operatorPass, mysql.ServiceName(cr))
		if ok, err := db.GetClusterSetReplicationRunning(ctx); err != nil {
			return false, errors.Wrap(err, "get cluster set replication running")
		} else if ok {
			return true, nil
		}
	}
	return false, nil
}
