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
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
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
	Recorder  record.EventRecorder

	getClusterSetManager clusterSetManagerGetter
	operatorPod          *corev1.Pod
}

type ClusterSetManager interface {
	CreateClusterSet(ctx context.Context, clustersetName string) error
	CreateReplicaCluster(ctx context.Context, cluster *apiv1.ClusterSetCluster) error
	RemoveReplicaCluster(ctx context.Context, clusterName string) error
	SetPrimaryCluster(ctx context.Context, clusterName string) error
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
	r.operatorPod = operatorPod

	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.PerconaServerMySQLClusterSet{}).
		Owns(&appsv1.Deployment{}).
		Owns(&batchv1.Job{}).
		Named(controllerName).
		Complete(r)
}

//+kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqlclustersets;perconaservermysqlclustersets/status;perconaservermysqlclustersets/finalizers,verbs=get;list;watch;create;update;patch;delete

// Reconcile reconciles the PerconaServerMySQLClusterSet custom resource.
func (r *PerconaServerMySQLClusterSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithName("PerconaServerMySQLClusterSet")

	pcs := &apiv1.PerconaServerMySQLClusterSet{}
	if err := r.Get(ctx, req.NamespacedName, pcs); err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, errors.Wrap(err, "get PerconaServerMySQLClusterSet")
	}

	if ready, err := r.reconcileMySQLShellRunner(ctx, pcs); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "reconcile MySQLShellRunner")
	} else if !ready {
		log.Info("Waiting for mysqlshell runner to be ready")
		return ctrl.Result{}, nil
	}

	manager, err := r.getClusterSetManager(ctx, pcs)
	if updErr := r.reconcilePrimaryClusterUnreachableCondition(ctx, pcs, err); updErr != nil {
		return ctrl.Result{}, errors.Wrap(updErr, "reconcile primary cluster unreachable condition")
	}

	if errors.Is(err, mysqlsh.ErrEndpointUnreachable) {
		if xerr := r.reconcileForcedFailover(ctx, pcs, manager); xerr != nil {
			return ctrl.Result{}, errors.Wrap(xerr, "reconcile forced failover")
		}
	} else if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "get cluster set manager")
	}

	if err := r.bootstrapClusterSet(ctx, pcs, manager); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "bootstrap ClusterSet")
	}

	if err := r.reconcileReplicas(ctx, pcs, manager); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "reconcile replicas")
	}

	if err := r.reconcileSwitchover(ctx, pcs, manager); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "reconcile switchover")
	}

	if err := r.reconcileStatus(ctx, pcs, manager); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "reconcile status")
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *PerconaServerMySQLClusterSetReconciler) reconcilePrimaryClusterUnreachableCondition(
	ctx context.Context,
	pcs *apiv1.PerconaServerMySQLClusterSet,
	csErr error,
) error {
	if errors.Is(csErr, mysqlsh.ErrEndpointUnreachable) {
		return pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) {
			meta.SetStatusCondition(&status.Conditions, metav1.Condition{
				Type:    apiv1.ConditionPrimaryClusterUnreachable,
				Status:  metav1.ConditionTrue,
				Reason:  apiv1.ConditionPrimaryClusterUnreachable,
				Message: "All primary cluster endpoints are unreachable",
			})
		})
	}

	cond := meta.FindStatusCondition(pcs.Status.Conditions, apiv1.ConditionPrimaryClusterUnreachable)
	if cond == nil {
		return nil
	}

	return pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) {
		meta.RemoveStatusCondition(&status.Conditions, apiv1.ConditionPrimaryClusterUnreachable)
	})
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

	if err := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) {
		meta.SetStatusCondition(&status.Conditions, cond)
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
	log := logf.FromContext(ctx).WithValues("primaryCluster", primaryCluster.Name)

	log.Info("Creating ClusterSet")
	if err := manager.CreateClusterSet(ctx, pcs.GetName()); err != nil {
		return errors.Wrap(err, "create cluster set")
	}

	log.Info("ClusterSet bootstrapped")
	r.Recorder.Event(pcs, corev1.EventTypeNormal, apiv1.EventTypeClusterSetBootstrapped, fmt.Sprintf("ClusterSet bootstrapped for primary cluster %s", primaryCluster.Name))

	if err := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:   apiv1.ConditionClusterSetBootstrapped,
			Status: metav1.ConditionTrue,
			Reason: "ClusterSetBootstrapped",
		})
		status.PrimaryCluster = primaryCluster.Name
	}); err != nil {
		return errors.Wrap(err, "update status")
	}
	return nil
}

//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;delete

func (r *PerconaServerMySQLClusterSetReconciler) reconcileReplicas(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet, manager ClusterSetManager) error {
	log := logf.FromContext(ctx)
	// Add replicas to the clusterset if not added already
	for _, cluster := range pcs.Spec.Clusters {
		if _, ok := pcs.Status.Clusters[cluster.Name]; !ok && cluster.Name != pcs.PrimaryCluster().Name {
			if err := r.addToClusterSet(ctx, pcs, &cluster); err != nil {
				return errors.Wrap(err, "add to cluster set")
			}
		}
	}

	if err := r.trackReplicaInitJobs(ctx, pcs); err != nil {
		return errors.Wrap(err, "track replica init jobs")
	}

	// Remove replicas from the clusterset if not present in spec
	for name := range pcs.Status.Clusters {
		if pcs.GetCluster(name) == nil && name != pcs.PrimaryCluster().Name {
			log.Info("Removing cluster from ClusterSet", "clusterName", name)
			if err := manager.RemoveReplicaCluster(ctx, name); err != nil {
				return errors.Wrap(err, "remove from cluster set")
			}
			log.Info("Removed cluster from ClusterSet", "clusterName", name)
		}
	}

	return nil
}

func (r *PerconaServerMySQLClusterSetReconciler) trackReplicaInitJobs(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) error {
	labels := naming.Labels(clusterset.ClusterSetReplicaInitAppName, pcs.Name, "percona-server", clusterset.ClusterSetReplicaInitComponent)

	jobs := &batchv1.JobList{}
	if err := r.List(ctx, jobs, client.InNamespace(pcs.Namespace), client.MatchingLabels(labels)); err != nil {
		return errors.Wrap(err, "list replica init jobs")
	}

	failed := []string{}

	for _, job := range jobs.Items {
		status, err := k8s.KStatusCompute(&job)
		if err != nil {
			return errors.Wrap(err, "compute status")
		}

		if status.Status == kstatus.FailedStatus {
			failed = append(failed, job.Labels["cluster-name"])
		}
	}

	if len(failed) == 0 && meta.IsStatusConditionTrue(pcs.Status.Conditions, apiv1.ConditionReplicaInitFailure) {
		if err := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) {
			meta.RemoveStatusCondition(&status.Conditions, apiv1.ConditionReplicaInitFailure)
		}); err != nil {
			return errors.Wrap(err, "update status")
		}
	} else if len(failed) > 0 {
		if err := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) {
			meta.SetStatusCondition(&status.Conditions, metav1.Condition{
				Type:    apiv1.ConditionReplicaInitFailure,
				Reason:  apiv1.ConditionReplicaInitFailure,
				Status:  metav1.ConditionTrue,
				Message: fmt.Sprintf("Replica init jobs failed for clusters: [%s]", strings.Join(failed, ", ")),
			})
		}); err != nil {
			return errors.Wrap(err, "update status")
		}
	}

	return nil
}

func (r *PerconaServerMySQLClusterSetReconciler) addToClusterSet(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet, cluster *apiv1.ClusterSetCluster) error {
	job := clusterset.ClusterSetReplicaInitJob(pcs, cluster, r.operatorPod.Spec.Containers[0].Image, r.operatorPod.Spec.ServiceAccountName)
	err := r.Get(ctx, client.ObjectKeyFromObject(job), job)

	log := logf.FromContext(ctx).WithValues("clusterName", cluster.Name)
	if k8serrors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(pcs, job, r.Scheme); err != nil {
			return errors.Wrap(err, "set controller reference")
		}
		log.Info("Adding to ClusterSet")
		return r.Create(ctx, job)
	} else if err != nil {
		return errors.Wrap(err, "get replica init job")
	}

	return nil
}

func (r *PerconaServerMySQLClusterSetReconciler) reconcileSwitchover(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet, manager ClusterSetManager) error {
	if pcs.Status.PrimaryCluster == "" || pcs.Spec.PrimaryCluster == pcs.Status.PrimaryCluster {
		return nil
	}

	log := logf.FromContext(ctx)
	log.Info("Switching primary cluster", "from", pcs.Status.PrimaryCluster, "to", pcs.Spec.PrimaryCluster)

	if err := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:    apiv1.ConditionClusterSetPrimarySwitchOverInProg,
			Status:  metav1.ConditionTrue,
			Reason:  apiv1.ConditionClusterSetPrimarySwitchOverInProg,
			Message: fmt.Sprintf("Primary cluster switchover in progress from %s to %s", pcs.Status.PrimaryCluster, pcs.Spec.PrimaryCluster),
		})
	}); err != nil {
		return errors.Wrap(err, "update status")
	}

	if err := manager.SetPrimaryCluster(ctx, pcs.Spec.PrimaryCluster); err != nil {
		if !errors.Is(err, csmanager.AlreadyPrimaryClusterError) {
			return errors.Wrap(err, "set primary cluster")
		}
	}

	if err := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) {
		status.PrimaryCluster = pcs.Spec.PrimaryCluster
		meta.RemoveStatusCondition(&status.Conditions, apiv1.ConditionClusterSetPrimarySwitchOverInProg)
	}); err != nil {
		return errors.Wrap(err, "update status")
	}

	r.Recorder.Event(pcs, corev1.EventTypeNormal, apiv1.EventTypeClusterSetPrimarySwitched,
		fmt.Sprintf("Primary cluster switched from %s to %s", pcs.Status.PrimaryCluster, pcs.Spec.PrimaryCluster),
	)

	return nil
}

func (r *PerconaServerMySQLClusterSetReconciler) reconcileForcedFailover(
	ctx context.Context,
	pcs *apiv1.PerconaServerMySQLClusterSet,
	manager ClusterSetManager,
) error {
	log := logf.FromContext(ctx)

	if pcs.Spec.PrimaryCluster == pcs.Status.PrimaryCluster {
		return nil
	}

	if pcs.Spec.AllowForcedFailover == nil || !*pcs.Spec.AllowForcedFailover {
		log.Info("Forced failover is requested, but not allowed. Set .spec.allowForcedFailover to true to allow it.")
		return nil
	}

	log.Info("Forcefully failing over primary cluster")

	// TODO

	r.Recorder.Event(pcs, corev1.EventTypeWarning, apiv1.EventTypeClusterSetPrimaryForcedSwitched,
		fmt.Sprintf("Primary cluster forced switched from %s to %s", pcs.Status.PrimaryCluster, pcs.Spec.PrimaryCluster))
	return nil
}

func (r *PerconaServerMySQLClusterSetReconciler) reconcileStatus(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet, manager ClusterSetManager) error {
	observedStatus, err := manager.Status(ctx)
	if err != nil {
		return errors.Wrap(err, "get cluster set status")
	}

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
		currentCond := meta.FindStatusCondition(pcs.Status.Conditions, apiv1.ConditionClusterSetReady)
		if currentCond == nil || currentCond.Status == metav1.ConditionTrue {
			r.Recorder.Event(pcs, corev1.EventTypeWarning, apiv1.EventTypeClusterSetUnhealthy,
				fmt.Sprintf("ClusterSet health degraded: %s", observedStatus.StatusText))
		}
	}

	if err := pcs.UpdateStatus(ctx, r.Client, func(status *apiv1.PerconaServerMySQLClusterSetStatus) {
		meta.SetStatusCondition(&status.Conditions, readyCond)
		status.Clusters = observedStatus.Clusters
		status.PrimaryClusterEndpoint = observedStatus.GlobalPrimaryInstance
	}); err != nil {
		return errors.Wrap(err, "update status")
	}
	return nil
}
