package psclusterset

import (
	"context"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kstatus "sigs.k8s.io/cli-utils/pkg/kstatus/status"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/clusterset"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"

	k8sutil "github.com/percona/percona-server-mysql-operator/pkg/util/k8s"
)

type clusterSetManagerGetter func(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) (ClusterSetManager, error)

type PerconaServerMySQLClusterSetReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	ClientCmd clientcmd.Client

	getClusterSetManager clusterSetManagerGetter
	operatorPod          *corev1.Pod
}

type ClusterSetManager interface {
	CreateClusterSet(ctx context.Context, clustersetName string) error
	CreateReplicaCluster(ctx context.Context, cluster *apiv1.ClusterSetCluster) error
	RemoveReplicaCluster(ctx context.Context, clusterName string) error
	SetPrimaryCluster(ctx context.Context, clusterName string) error
}

const controllerName = "psclusterset-controller"

func (r *PerconaServerMySQLClusterSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.getClusterSetManager == nil {
		r.getClusterSetManager = func(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) (ClusterSetManager, error) {
			return clusterset.NewManager(ctx, r.Client, r.ClientCmd, pcs)
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

// SetupWithManager sets up the controller with the Manager.
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
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "get cluster set manager")
	}

	if err := r.bootstrapClusterSet(ctx, pcs, manager); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "bootstrap ClusterSet")
	}

	if err := r.reconcileReplicas(ctx, pcs, manager); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "reconcile replicas")
	}

	if err := r.reconcileSwitchover(ctx, pcs); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "reconcile switchover")
	}

	if err := r.reconcileStatus(ctx, pcs); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "reconcile status")
	}

	return ctrl.Result{}, nil
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

func (r *PerconaServerMySQLClusterSetReconciler) reconcileReplicas(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet, manager ClusterSetManager) error {
	log := logf.FromContext(ctx)
	// Add replicas to the clusterset if not added already
	for _, cluster := range pcs.Spec.Clusters {
		if _, ok := pcs.Status.Clusters[cluster.Name]; !ok {
			log.Info("Adding cluster to ClusterSet", "clusterName", cluster.Name)
			if err := r.addToClusterSet(ctx, pcs, &cluster); err != nil {
				return errors.Wrap(err, "add to cluster set")
			}
		}
	}

	// Remove replicas from the clusterset if not present in spec
	for name := range pcs.Status.Clusters {
		if pcs.GetCluster(name) == nil {
			log.Info("Removing cluster from ClusterSet", "clusterName", name)
			if err := manager.RemoveReplicaCluster(ctx, name); err != nil {
				return errors.Wrap(err, "remove from cluster set")
			}
		}
	}

	return nil
}

func (r *PerconaServerMySQLClusterSetReconciler) addToClusterSet(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet, cluster *apiv1.ClusterSetCluster) error {
	job := clusterset.ClusterSetReplicaInitJob(pcs, cluster, r.operatorPod.Spec.Containers[0].Image, r.operatorPod.Spec.ServiceAccountName)
	err := r.Get(ctx, client.ObjectKeyFromObject(job), job)
	if k8serrors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(pcs, job, r.Scheme); err != nil {
			return errors.Wrap(err, "set controller reference")
		}
		return r.Create(ctx, job)
	} else if err != nil {
		return errors.Wrap(err, "get replica init job")
	}

	return nil
}

func (r *PerconaServerMySQLClusterSetReconciler) reconcileSwitchover(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) error {
	return nil
}

func (r *PerconaServerMySQLClusterSetReconciler) reconcileStatus(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) error {
	return nil
}
