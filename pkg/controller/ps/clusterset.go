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
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// checkClusterSetStatus updates the status conditions for ClusterSet members.
func (r *PerconaServerMySQLReconciler) checkClusterSetStatus(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	status *apiv1.PerconaServerMySQLStatus, // all status updates must happen here
) error {
	// remove legacy condition
	meta.RemoveStatusCondition(&status.Conditions, apiv1.ConditionClusterSetReplicationRunning)

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
		log.Info("No pods available to query, skip ClusterSet status check")
		return nil
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1.UserOperator)
	if k8serrors.IsNotFound(err) {
		log.Info("Cannot reconcile clusterset status, wait for internal secrets to be created")
		return nil
	} else if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	clusterRole, err := r.getInnoDBClusterRole(ctx, cr, operatorPass, pods[0])
	if err != nil {
		return errors.Wrap(err, "get InnoDB cluster role")
	}

	// ClusterRole is one of PRIMARY or REPLICA when member of a ClusterSet. Empty when it is an independent cluster.
	switch clusterRole {
	case clusterset.ClusterRolePrimary:
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:    apiv1.ConditionClusterSetMember,
			Status:  metav1.ConditionTrue,
			Reason:  apiv1.ClusterSetMemberReasonPrimary,
			Message: "Cluster is a primary member of ClusterSet",
		})
		return nil
	case clusterset.ClusterRoleReplica:
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:    apiv1.ConditionClusterSetMember,
			Status:  metav1.ConditionTrue,
			Reason:  apiv1.ClusterSetMemberReasonReplica,
			Message: "Cluster is a replica member of ClusterSet",
		})
		return nil
	}

	// The cluster is not a clusterset member, neither was it a part of it before. Nothing to do.
	if meta.FindStatusCondition(cr.Status.Conditions, apiv1.ConditionClusterSetMember) == nil {
		return nil
	}

	// Cluster was a former member of ClusterSet, we need to wait for GR to be dissolved,
	// and re-bootstrap the cluster.
	if len(pods) < int(cr.MySQLSpec().Size) {
		return nil
	}
	for _, pod := range pods {
		if !k8s.IsPodReady(pod) {
			return nil
		}
	}

	db := database.NewReplicationManager(&pods[0], r.ClientCmd, apiv1.UserOperator, operatorPass, mysql.ServiceName(cr))
	members, err := db.GetGroupReplicationMembers(ctx)
	if err != nil {
		return errors.Wrap(err, "get group replication members")
	}

	// Wait for mysqlshell to completely dissolve the cluster.
	clusterDissolved := func() bool {
		return len(members) == 0 || (len(members) == 1 && members[0].MemberState == innodbcluster.MemberStateOffline)
	}
	if !clusterDissolved() {
		return nil
	}

	log.Info("Former ClusterSet member, recovery is needed")
	if err := k8s.AnnotateObject(ctx, r.Client, cr, map[naming.AnnotationKey]string{
		naming.AnnotationClusterSetRecoveryNeeded: "true",
	}); err != nil {
		return errors.Wrap(err, "add clusterset recovery annotation")
	}

	if err := k8s.RemoveFinalizers(ctx, r.Client, cr, naming.FinalizerClusterSetProtection); err != nil {
		return errors.Wrap(err, "set finalizer")
	}
	meta.RemoveStatusCondition(&status.Conditions, apiv1.ConditionClusterSetReplicationRunning)
	return nil
}

func (r *PerconaServerMySQLReconciler) reconcileClusterSetRecovery(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
) error {
	if val, ok := cr.GetAnnotations()[string(naming.AnnotationClusterSetRecoveryNeeded)]; !ok || val != "true" {
		return nil
	}

	log := logf.FromContext(ctx)

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr), cr.Namespace)
	if err != nil {
		return errors.Wrap(err, "get pods")
	}

	if cr.Spec.Pause || len(pods) == 0 {
		log.Info("No pods available for ClusterSet recovery")
		return nil
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	log.Info("Recovering former ClusterSet member")
	if err := r.recoverClustersetReplicaCluster(ctx, cr, operatorPass, pods); err != nil {
		return errors.Wrap(err, "recover clusterset replica cluster")
	}

	if err := k8s.DeannotateObject(ctx, r.Client, cr, naming.AnnotationClusterSetRecoveryNeeded); err != nil {
		return errors.Wrap(err, "deannotate object")
	}
	return nil
}

func (r *PerconaServerMySQLReconciler) getInnoDBClusterRole(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	operatorPass string,
	pod corev1.Pod,
) (string, error) {
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
		return "", errors.Wrap(err, "new mysqlsh")
	}

	status, err := shell.ClusterStatusWithExec(ctx)
	if err != nil {
		// GR dissolution has already started
		if strings.Contains(err.Error(), "not available through a session to a standalone instance") {
			return "", nil
		}
		return "", errors.Wrap(err, "get cluster status")
	}

	return status.ClusterRole, nil
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
