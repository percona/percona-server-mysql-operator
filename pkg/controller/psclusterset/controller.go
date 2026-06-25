package psclusterset

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	kstatus "sigs.k8s.io/cli-utils/pkg/kstatus/status"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/clusterset"
	csmanager "github.com/percona/percona-server-mysql-operator/pkg/clusterset/manager"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	k8sutil "github.com/percona/percona-server-mysql-operator/pkg/util/k8s"
)

type clusterSetManagerGetter func(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) (ClusterSetManager, error)

type PerconaServerMySQLClusterSetReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	ClientCmd clientcmd.Client
	Recorder  events.EventRecorder

	getClusterSetManager clusterSetManagerGetter
	jobImage             string
}

type ClusterSetManager interface {
	CreateClusterSet(ctx context.Context, clustersetName string, sslMode apiv1.ClusterSetSSLMode) error
	ForcePrimaryCluster(ctx context.Context, clusterName string) error
	Status(ctx context.Context) (clusterset.Status, error)
}

const controllerName = "psclusterset-controller"

func (r *PerconaServerMySQLClusterSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.getClusterSetManager == nil {
		r.getClusterSetManager = func(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) (ClusterSetManager, error) {
			opts := &csmanager.ManagerOptions{
				Client:    r.Client,
				ClientCmd: r.ClientCmd,
				Stdout:    &bytes.Buffer{},
			}
			return csmanager.New(ctx, pcs, opts)
		}
	}

	operatorPod, err := k8sutil.GetOperatorPod(context.Background(), mgr.GetAPIReader())
	if err != nil {
		return errors.Wrap(err, "get operator pod")
	}
	r.jobImage = operatorPod.Spec.Containers[0].Image

	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.PerconaServerMySQLClusterSet{}).
		Owns(&appsv1.Deployment{}).
		Owns(&batchv1.Job{}).
		Named(controllerName).
		Complete(r)
}

var errGetClusterSetManager = errors.New("get cluster set manager")

//+kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqlclustersets;perconaservermysqlclustersets/status;perconaservermysqlclustersets/finalizers,verbs=get;list;watch;create;update;patch;delete

// Reconcile reconciles the PerconaServerMySQLClusterSet custom resource.
func (r *PerconaServerMySQLClusterSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, retErr error) {
	log := logf.FromContext(ctx).WithName("PerconaServerMySQLClusterSet")

	pcs := &apiv1.PerconaServerMySQLClusterSet{}
	if err := r.Get(ctx, req.NamespacedName, pcs); err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, errors.Wrap(err, "get PerconaServerMySQLClusterSet")
	}

	pcs.SetDefaults()

	if !pcs.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, r.reconcileFinalizers(ctx, pcs)
	}

	var err error
	defer func() {
		if updErr := r.reconcileErrorCondition(ctx, pcs, err); updErr != nil && retErr == nil {
			retErr = errors.Wrap(updErr, "reconcile error condition")
		}
	}()

	if err = r.reconcileRBAC(ctx, pcs); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "reconcile RBAC")
	}

	if err = r.cleanupOutdatedJobs(ctx, pcs); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "cleanup outdated jobs")
	}

	var runnerReady bool
	if runnerReady, err = r.reconcileMySQLShellRunner(ctx, pcs); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "reconcile MySQLShellRunner")
	} else if !runnerReady {
		log.Info("Waiting for mysqlshell runner to be ready")
		return ctrl.Result{}, nil
	}

	var manager ClusterSetManager
	manager, err = r.getClusterSetManager(ctx, pcs)
	if errors.Is(err, mysqlsh.ErrEndpointUnreachable) {
		if ffErr := r.reconcileForcedFailover(ctx, pcs); ffErr != nil {
			err = errors.Wrap(ffErr, "reconcile forced failover")
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	} else if err != nil {
		err = fmt.Errorf("%w: %w", errGetClusterSetManager, err)
		return ctrl.Result{}, err
	}

	if err = r.bootstrapClusterSet(ctx, pcs, manager); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "bootstrap ClusterSet")
	}

	if err = r.reconcileReplicas(ctx, pcs); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "reconcile replicas")
	}

	if err = r.reconcileSwitchover(ctx, pcs); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "reconcile switchover")
	}

	if err = r.reconcileStatus(ctx, pcs, manager); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "reconcile status")
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *PerconaServerMySQLClusterSetReconciler) reconcileErrorCondition(
	ctx context.Context,
	pcs *apiv1.PerconaServerMySQLClusterSet,
	rErr error,
) error {
	if rErr == nil && meta.FindStatusCondition(pcs.Status.Conditions, apiv1.ConditionClusterSetErrorReconcile) == nil {
		return nil
	}

	markAllNodesUnknown := func(status *apiv1.PerconaServerMySQLClusterSetStatus) {
		for name, c := range status.Clusters {
			c.GlobalStatus = clusterset.StatusUnknown
			status.Clusters[name] = c
		}
	}

	if updErr := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) error {
		if rErr == nil {
			meta.RemoveStatusCondition(&status.Conditions, apiv1.ConditionClusterSetErrorReconcile)
			return nil
		}

		accessDenied := errors.Is(rErr, mysqlsh.ErrAccessDenied)
		unreachable := errors.Is(rErr, mysqlsh.ErrEndpointUnreachable)
		lostPrimaryConnection := accessDenied || unreachable || errors.Is(rErr, errGetClusterSetManager)

		errCond := metav1.Condition{
			Type:    apiv1.ConditionClusterSetErrorReconcile,
			Status:  metav1.ConditionTrue,
			Message: rErr.Error(),
			Reason:  "ReconcileError",
		}
		switch {
		case accessDenied:
			errCond.Reason = "AccessDenied"
			errCond.Message = "Access denied on primary, check the clusterset credentials"
		case unreachable:
			errCond.Reason = "PrimaryClusterUnreachable"
			errCond.Message = "Primary cluster is unreachable, check the network and cluster status"
		}
		meta.SetStatusCondition(&status.Conditions, errCond)

		if lostPrimaryConnection {
			markAllNodesUnknown(status)
			meta.SetStatusCondition(&status.Conditions, metav1.Condition{
				Type:    apiv1.ConditionClusterSetReady,
				Status:  metav1.ConditionUnknown,
				Message: fmt.Sprintf("Error connecting to primary cluster: %s", rErr.Error()),
				Reason:  "ClusterSetManagerError",
			})
		}
		return nil
	}); updErr != nil {
		return errors.Wrap(updErr, "update status")
	}
	return nil
}

//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete

// reconcileMySQLShellRunner reconciles the MySQLShellRunner deployment for the ClusterSet.
func (r *PerconaServerMySQLClusterSetReconciler) reconcileMySQLShellRunner(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) (bool, error) {
	desired := clusterset.MySQLShellRunner(pcs)
	actual := desired.DeepCopy()
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, actual, func() error {
		if err := controllerutil.SetControllerReference(pcs, actual, r.Scheme); err != nil {
			return errors.Wrap(err, "set controller reference")
		}
		actual.Spec = desired.Spec
		actual.Labels = desired.Labels
		return nil
	}); err != nil {
		return false, errors.Wrap(err, "create or update MySQLShellRunner")
	}

	if err := r.Get(ctx, client.ObjectKeyFromObject(actual), actual); err != nil {
		return false, errors.Wrap(err, "get mysqlshell runner deployment")
	}

	status, err := k8s.KStatusCompute(actual)
	if err != nil {
		return false, errors.Wrap(err, "compute status")
	}

	cond := metav1.Condition{
		Type:   apiv1.ConditionMySQLShellRunnerReady,
		Status: metav1.ConditionUnknown,
		Reason: "DeploymentNotObserved",
	}

	ready := false
	switch status.Status {
	case kstatus.CurrentStatus:
		cond.Status = metav1.ConditionTrue
		cond.Reason = "DeploymentReady"
		ready = true
	case kstatus.InProgressStatus:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "DeploymentNotReady"
	case kstatus.FailedStatus:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "DeploymentFailed"
	}

	if err := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) error {
		meta.SetStatusCondition(&status.Conditions, cond)
		return nil
	}); err != nil {
		return false, errors.Wrap(err, "update status")
	}
	return ready, nil
}

func (r *PerconaServerMySQLClusterSetReconciler) bootstrapClusterSet(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet, manager ClusterSetManager) error {
	// Already bootstrapped, early return.
	if meta.IsStatusConditionTrue(pcs.Status.Conditions, apiv1.ConditionClusterSetBootstrapped) {
		return nil
	}

	primaryCluster := pcs.PrimaryCluster()
	if primaryCluster == nil {
		return errors.New("primary cluster not found in spec")
	}

	log := logf.FromContext(ctx).WithValues("primaryCluster", primaryCluster.InnoDBClusterName)

	sslMode := apiv1.ClusterSetSSLModeAuto
	if pcs.Spec.SSLMode != nil {
		sslMode = *pcs.Spec.SSLMode
	}
	log.Info("Creating ClusterSet")
	if err := manager.CreateClusterSet(ctx, pcs.GetName(), sslMode); err != nil {
		return errors.Wrap(err, "create cluster set")
	}

	log.Info("ClusterSet bootstrapped")

	r.Recorder.Eventf(pcs, nil, corev1.EventTypeNormal, apiv1.EventTypeClusterSetBootstrapped,
		apiv1.EventTypeClusterSetBootstrapped, "ClusterSet bootstrapped for primary cluster %s", primaryCluster.InnoDBClusterName)

	if err := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) error {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:   apiv1.ConditionClusterSetBootstrapped,
			Status: metav1.ConditionTrue,
			Reason: "ClusterSetBootstrapped",
		})
		status.PrimaryCluster = primaryCluster.InnoDBClusterName
		return nil
	}); err != nil {
		return errors.Wrap(err, "update status")
	}
	return nil
}

//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch

func (r *PerconaServerMySQLClusterSetReconciler) reconcileReplicas(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) error {
	// Add replicas to the clusterset if not added already
	for _, cluster := range pcs.Spec.Clusters {
		if _, ok := pcs.Status.Clusters[cluster.InnoDBClusterName]; !ok && cluster.InnoDBClusterName != pcs.PrimaryCluster().InnoDBClusterName {
			if err := r.runClusterSetJob(ctx, clusterset.CmdAddReplica, pcs, &cluster); err != nil {
				return errors.Wrap(err, "add replica clusters")
			}
		}
	}

	// Remove replicas from the clusterset if not present in spec
	for name := range pcs.Status.Clusters {
		if pcs.GetCluster(name) == nil && name != pcs.PrimaryCluster().InnoDBClusterName {
			if err := r.runClusterSetJob(ctx, clusterset.CmdRemoveReplica, pcs, &apiv1.ClusterSetCluster{InnoDBClusterName: name}); err != nil {
				return errors.Wrap(err, "remove replica clusters")
			}
		}
	}

	if err := r.trackReplicaTopologyChanges(ctx, pcs); err != nil {
		return errors.Wrap(err, "track replica manager jobs")
	}

	return nil
}

func (r *PerconaServerMySQLClusterSetReconciler) trackSwitchover(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) error {
	labels := naming.Labels(clusterset.ClusterSetReplicaManagerAppName, pcs.Name, "percona-server", clusterset.ClusterSetReplicaManagerComponent)
	labels["command"] = clusterset.CmdSetPrimary
	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace(pcs.Namespace), client.MatchingLabels(labels)); err != nil {
		return errors.Wrap(err, "list replica init jobs")
	}

	if err := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) error {
		meta.RemoveStatusCondition(&status.Conditions, apiv1.ConditionClusterSetPrimarySwitchOverInProg)
		for _, job := range jobs.Items {
			switch {
			// Job has completed
			case jobConditionTrue(&job, batchv1.JobComplete):
				continue

			// Job has failed
			case jobConditionTrue(&job, batchv1.JobFailed):
				meta.SetStatusCondition(&status.Conditions, metav1.Condition{
					Type:    apiv1.ConditionClusterSetPrimarySwitchOverInProg,
					Status:  metav1.ConditionFalse,
					Reason:  "SwitchoverFailed",
					Message: "Switchover failed",
				})
				return nil

			// Job is still running
			default:
				meta.SetStatusCondition(&status.Conditions, metav1.Condition{
					Type:    apiv1.ConditionClusterSetPrimarySwitchOverInProg,
					Status:  metav1.ConditionTrue,
					Reason:  "SwitchoverInProgress",
					Message: "Switchover in progress",
				})
				continue
			}
		}
		return nil
	}); err != nil {
		return errors.Wrap(err, "update status")
	}
	return nil
}

func jobConditionTrue(job *batchv1.Job, condType batchv1.JobConditionType) bool {
	for _, cond := range job.Status.Conditions {
		if cond.Type == condType && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *PerconaServerMySQLClusterSetReconciler) trackReplicaTopologyChanges(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) error {
	labels := naming.Labels(clusterset.ClusterSetReplicaManagerAppName, pcs.Name, "percona-server", clusterset.ClusterSetReplicaManagerComponent)

	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace(pcs.Namespace), client.MatchingLabels(labels)); err != nil {
		return errors.Wrap(err, "list replica init jobs")
	}

	var (
		failedAdd    = []string{}
		failedRemove = []string{}
	)

	for _, job := range jobs.Items {
		status, err := k8s.KStatusCompute(&job)
		if err != nil {
			return errors.Wrap(err, "compute status")
		}

		if status.Status == kstatus.FailedStatus {
			switch job.Labels["command"] {
			case clusterset.CmdAddReplica:
				failedAdd = append(failedAdd, job.Labels["cluster-name"])
			case clusterset.CmdRemoveReplica:
				failedRemove = append(failedRemove, job.Labels["cluster-name"])
			}
		}
	}

	message := fmt.Sprintf("failed to add clusters: [%s], failed to remove clusters: [%s]", strings.Join(failedAdd, ", "), strings.Join(failedRemove, ", "))
	if len(failedAdd) > 0 || len(failedRemove) > 0 {
		if err := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) error {
			meta.SetStatusCondition(&status.Conditions, metav1.Condition{
				Type:    apiv1.ConditionReplicaManagementFailure,
				Reason:  apiv1.ConditionReplicaManagementFailure,
				Status:  metav1.ConditionTrue,
				Message: fmt.Sprintf("Replica management failure: %s", message),
			})
			return nil
		}); err != nil {
			return errors.Wrap(err, "update status")
		}
		return nil
	}

	if cond := meta.FindStatusCondition(pcs.Status.Conditions, apiv1.ConditionReplicaManagementFailure); cond != nil {
		if err := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) error {
			meta.RemoveStatusCondition(&status.Conditions, apiv1.ConditionReplicaManagementFailure)
			return nil
		}); err != nil {
			return errors.Wrap(err, "update status")
		}
	}
	return nil
}

func (r *PerconaServerMySQLClusterSetReconciler) runClusterSetJob(
	ctx context.Context,
	command string,
	pcs *apiv1.PerconaServerMySQLClusterSet,
	cluster *apiv1.ClusterSetCluster,
) error {

	args := []string{}
	args = append(args, "--ps-cluster-set-name="+pcs.Name)
	args = append(args, "--namespace="+pcs.Namespace)

	switch command {
	case clusterset.CmdAddReplica:
		args = append(args, "--replica-cluster-name="+cluster.InnoDBClusterName)
		args = append(args, "--replica-endpoint="+cluster.Endpoints[0].Host)
		args = append(args, "--replica-port="+fmt.Sprintf("%d", cluster.Endpoints[0].GetPort()))
		args = append(args, "--recovery-method="+string(pcs.Spec.CreateReplicaClusterOptions.RecoveryMethod))
	case clusterset.CmdRemoveReplica:
		args = append(args, "--replica-cluster-name="+cluster.InnoDBClusterName)
	}

	job := clusterset.ClusterSetManagerJob(pcs, cluster, command, args, r.jobImage, clustersetServiceAccount(pcs).Name)
	err := r.Get(ctx, client.ObjectKeyFromObject(job), job)

	verb := "Adding to"
	if command == clusterset.CmdRemoveReplica {
		verb = "Removing from"
	}

	log := logf.FromContext(ctx).WithValues("clusterName", cluster.InnoDBClusterName)
	if k8serrors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(pcs, job, r.Scheme); err != nil {
			return errors.Wrap(err, "set controller reference")
		}
		log.Info(fmt.Sprintf("%s ClusterSet", verb))
		return r.Create(ctx, job)
	} else if err != nil {
		return errors.Wrap(err, "get replica manager job")
	}

	return nil
}

func (r *PerconaServerMySQLClusterSetReconciler) reconcileSwitchover(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) error {
	if pcs.Status.PrimaryCluster == "" || pcs.Spec.PrimaryCluster == pcs.Status.PrimaryCluster {
		return nil
	}

	log := logf.FromContext(ctx)
	log.Info("Switching primary cluster", "from", pcs.Status.PrimaryCluster, "to", pcs.Spec.PrimaryCluster)

	var cluster *apiv1.ClusterSetCluster
	for i := range pcs.Spec.Clusters {
		if pcs.Spec.Clusters[i].InnoDBClusterName == pcs.Spec.PrimaryCluster {
			cluster = &pcs.Spec.Clusters[i]
			break
		}
	}
	if cluster == nil {
		return errors.New("primary cluster not found in spec")
	}

	if err := r.runClusterSetJob(ctx, clusterset.CmdSetPrimary, pcs, cluster); err != nil {
		return err
	}
	return nil
}

func (r *PerconaServerMySQLClusterSetReconciler) reconcileForcedFailover(
	ctx context.Context,
	pcs *apiv1.PerconaServerMySQLClusterSet,
) error {
	log := logf.FromContext(ctx)

	if pcs.Spec.PrimaryCluster == pcs.Status.PrimaryCluster {
		return nil
	}

	allowed := pcs.Spec.UnsafeClusterSetFlags.ForcedFailover != nil && *pcs.Spec.UnsafeClusterSetFlags.ForcedFailover
	if !allowed {
		log.Info("Forced failover is requested, but not allowed. Set .spec.unsafeFlags.forcedFailover to true to allow it.")
		return nil
	}

	log.Info("Initiating forced failover")

	temp := pcs.DeepCopy()
	temp.Status.PrimaryCluster = ""
	manager, err := r.getClusterSetManager(ctx, temp)
	if err != nil {
		return errors.Wrap(err, "get cluster set manager")
	}

	if err := manager.ForcePrimaryCluster(ctx, pcs.Spec.PrimaryCluster); err != nil {
		return errors.Wrap(err, "force primary cluster")
	}

	if err := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) error {
		status.PrimaryCluster = pcs.Spec.PrimaryCluster
		return nil
	}); err != nil {
		return errors.Wrap(err, "update status")
	}

	r.Recorder.Eventf(pcs, nil, corev1.EventTypeWarning, apiv1.EventTypeClusterSetPrimaryForcedSwitched,
		apiv1.EventTypeClusterSetPrimaryForcedSwitched, "Primary cluster forcefully switched from %s to %s", pcs.Status.PrimaryCluster, pcs.Spec.PrimaryCluster)

	return nil
}

// Returns the names of clusters that are in a but not in b.
func clusterSetMemberDiff(a, b apiv1.ClusterSetStatus) []string {
	diff := []string{}
	for name := range a {
		if _, ok := b[name]; !ok {
			diff = append(diff, name)
		}
	}
	return diff
}

func (r *PerconaServerMySQLClusterSetReconciler) reconcileStatus(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet, manager ClusterSetManager) error {
	observedStatus, err := manager.Status(ctx)
	if err != nil {
		return errors.Wrap(err, "get cluster set status")
	}

	if err := r.trackSwitchover(ctx, pcs); err != nil {
		return errors.Wrap(err, "track switchover")
	}

	// Since event decisions are made based on the current status, we need to ensure that we are not reading a stale status from cache.
	// Since UpdateStatus is retried on conflicts, we only keep track of which events need to be emitted based.
	// Once the status update is successful, we know that all decesions were made based on an update state and can emit the events.
	events := []func(){}

	if err := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) error {
		events = []func(){}

		readyCond := metav1.Condition{
			Type:    apiv1.ConditionClusterSetReady,
			Status:  metav1.ConditionTrue,
			Reason:  "ClusterSetHealthy",
			Message: observedStatus.StatusText,
		}
		if observedStatus.Status != clusterset.StatusHealthy {
			readyCond.Status = metav1.ConditionFalse
			readyCond.Reason = "ClusterSetNotHealthy"

			// Emit an event when we transition from healthy to unhealthy
			currentCond := meta.FindStatusCondition(status.Conditions, apiv1.ConditionClusterSetReady)
			if currentCond == nil || currentCond.Status == metav1.ConditionTrue {
				events = append(events, func() {
					r.Recorder.Eventf(pcs, nil, corev1.EventTypeWarning, apiv1.EventTypeClusterSetUnhealthy,
						apiv1.EventTypeClusterSetUnhealthy, "ClusterSet health degraded: %s", observedStatus.StatusText)
				})
			}
		}
		meta.SetStatusCondition(&status.Conditions, readyCond)

		// Emit an event for each newly added member
		newlyAdded := clusterSetMemberDiff(observedStatus.Clusters, status.Clusters)
		for _, name := range newlyAdded {
			events = append(events, func() {
				r.Recorder.Eventf(pcs, nil, corev1.EventTypeNormal, apiv1.EventTypeClusterSetMemberAdded,
					apiv1.EventTypeClusterSetMemberAdded, "Cluster %s added to ClusterSet with role %s", name, observedStatus.Clusters[name].ClusterRole)
			})
		}

		// Emit an event for each newly removed member
		newlyRemoved := clusterSetMemberDiff(status.Clusters, observedStatus.Clusters)
		for _, name := range newlyRemoved {
			events = append(events, func() {
				r.Recorder.Eventf(pcs, nil, corev1.EventTypeNormal, apiv1.EventTypeClusterSetMemberRemoved, apiv1.EventTypeClusterSetMemberRemoved, "Cluster %s removed from ClusterSet", name)
			})
		}

		// Emit an event when the primary cluster is switched
		if status.PrimaryCluster != "" && status.PrimaryCluster != observedStatus.PrimaryCluster {
			oldPrimaryCluster := status.PrimaryCluster
			newPrimaryCluster := observedStatus.PrimaryCluster
			events = append(events, func() {
				r.Recorder.Eventf(pcs, nil, corev1.EventTypeNormal, apiv1.EventTypeClusterSetPrimarySwitched, apiv1.EventTypeClusterSetPrimarySwitched, "Primary cluster switched from %s to %s", oldPrimaryCluster, newPrimaryCluster)
			})
		}

		status.Clusters = observedStatus.Clusters
		status.PrimaryCluster = observedStatus.PrimaryCluster
		status.PrimaryClusterEndpoint = observedStatus.GlobalPrimaryInstance
		return nil
	}); err != nil {
		return errors.Wrap(err, "update status")
	}

	for _, emitEvt := range events {
		emitEvt()
	}
	return nil
}

func (r *PerconaServerMySQLClusterSetReconciler) reconcileFinalizers(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) error {
	updated := []string{}
	for _, finalizer := range pcs.GetFinalizers() {
		switch finalizer {
		case naming.FinalizerClusterSetDissolve:
			manager, err := r.getClusterSetManager(ctx, pcs)
			if err != nil {
				return errors.Wrap(err, "get cluster set manager")
			}
			if ok, err := r.dissolveClusterSet(ctx, pcs, manager); err != nil {
				return errors.Wrap(err, "dissolve cluster set")
			} else if !ok {
				updated = append(updated, finalizer)
			}
		default:
			updated = append(updated, finalizer)
		}
	}

	orig := pcs.DeepCopy()
	pcs.SetFinalizers(updated)
	if err := r.Patch(ctx, pcs, client.MergeFrom(orig)); err != nil {
		return errors.Wrap(err, "patch cluster set finalizers")
	}

	return nil
}

// dissolveClusterSet dissolves the cluster set by removing all replicas.
// mysqlshell 9.7 natively supports this operation, but this version is not supported in the operator at the time of writing this.
func (r *PerconaServerMySQLClusterSetReconciler) dissolveClusterSet(
	ctx context.Context,
	pcs *apiv1.PerconaServerMySQLClusterSet,
	manager ClusterSetManager,
) (bool, error) {
	observedStatus, err := manager.Status(ctx)
	if err != nil {
		return false, errors.Wrap(err, "get cluster set status")
	}
	clusters := observedStatus.Clusters

	if err := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) error {
		status.Clusters = observedStatus.Clusters
		status.Conditions = []metav1.Condition{}
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:    apiv1.ConditionClusterSetDissolving,
			Status:  metav1.ConditionTrue,
			Reason:  apiv1.ConditionClusterSetDissolving,
			Message: fmt.Sprintf("Dissolving clusterset, %d cluster(s) remaining", len(clusters)-1),
		})
		return nil
	}); err != nil {
		return false, errors.Wrap(err, "update status")
	}

	// If only one cluster remains, that's the primary. Removal is done.
	if len(clusters) == 1 {
		return true, nil
	}

	// Remove all replicas from the clusterset
	for name, cluster := range clusters {
		if cluster.ClusterRole == clusterset.ClusterRolePrimary {
			continue
		}
		if err := r.runClusterSetJob(ctx, clusterset.CmdRemoveReplica, pcs, &apiv1.ClusterSetCluster{InnoDBClusterName: name}); err != nil {
			return false, errors.Wrap(err, "run replica manager job")
		}
	}

	return false, nil
}

func clustersetRole(pcs *apiv1.PerconaServerMySQLClusterSet) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-clusterset", pcs.Name),
			Namespace: pcs.Namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods/exec"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"get"},
			},
			{
				APIGroups: []string{apiv1.GroupVersion.Group},
				Resources: []string{"perconaservermysqlclustersets"},
				Verbs:     []string{"get"},
			},
		},
	}
}

func clustersetRoleBinding(pcs *apiv1.PerconaServerMySQLClusterSet) *rbacv1.RoleBinding {
	role := clustersetRole(pcs)
	serviceAccount := clustersetServiceAccount(pcs)
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-clusterset", pcs.Name),
			Namespace: pcs.Namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "Role",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      serviceAccount.Name,
				Namespace: serviceAccount.Namespace,
			},
		},
	}
}

func clustersetServiceAccount(pcs *apiv1.PerconaServerMySQLClusterSet) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-clusterset", pcs.Name),
			Namespace: pcs.Namespace,
		},
	}
}

func (r *PerconaServerMySQLClusterSetReconciler) reconcileRBAC(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) error {
	role := clustersetRole(pcs)
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		if err := controllerutil.SetControllerReference(pcs, role, r.Scheme); err != nil {
			return err
		}
		role.Rules = clustersetRole(pcs).Rules
		return nil
	}); err != nil {
		return errors.Wrap(err, "create or update role")
	}

	roleBinding := clustersetRoleBinding(pcs)
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, roleBinding, func() error {
		if err := controllerutil.SetControllerReference(pcs, roleBinding, r.Scheme); err != nil {
			return err
		}
		roleBinding.RoleRef = clustersetRoleBinding(pcs).RoleRef
		roleBinding.Subjects = clustersetRoleBinding(pcs).Subjects
		return nil
	}); err != nil {
		return errors.Wrap(err, "create or update role binding")
	}

	serviceAccount := clustersetServiceAccount(pcs)
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, serviceAccount, func() error {
		if err := controllerutil.SetControllerReference(pcs, serviceAccount, r.Scheme); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return errors.Wrap(err, "create or update service account")
	}
	return nil
}

func (r *PerconaServerMySQLClusterSetReconciler) cleanupOutdatedJobs(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) error {
	labels := naming.Labels(clusterset.ClusterSetReplicaManagerAppName, pcs.Name, "percona-server", clusterset.ClusterSetReplicaManagerComponent)
	jobs := &batchv1.JobList{}

	if err := r.List(ctx, jobs, client.InNamespace(pcs.Namespace), client.MatchingLabels(labels)); err != nil {
		return errors.Wrap(err, "list replica manager jobs")
	}

	for _, job := range jobs.Items {
		if job.Spec.TTLSecondsAfterFinished != nil {
			continue
		}
		if jobConditionTrue(&job, batchv1.JobComplete) {
			// Patch the Job with a TTLSecondsAfterFinished so that deletion is deferred.
			// Immediate deletion can result in duplicate jobs if the informer cache is not updated fast enough.
			orig := job.DeepCopy()
			job.Spec.TTLSecondsAfterFinished = new(int32(30))
			if err := r.Patch(ctx, &job, client.MergeFrom(orig)); err != nil {
				return errors.Wrap(err, "patch replica manager job")
			}
		}
	}
	return nil
}
