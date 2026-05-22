package psclusterset

import (
	"context"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
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
	"github.com/percona/percona-server-mysql-operator/pkg/clusterset"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

type PerconaServerMySQLClusterSetReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const controllerName = "psclusterset-controller"

func (r *PerconaServerMySQLClusterSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
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

	if bootstrapped, err := r.bootstrapClusterSet(ctx, pcs); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "bootstrap ClusterSet")
	} else if !bootstrapped {
		log.Info("Waiting for ClusterSet to be bootstrapped")
		return ctrl.Result{}, nil
	}

	if configured, err := r.configureReplicas(ctx, pcs); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "configure replicas")
	} else if !configured {
		log.Info("Waiting for replicas to be configured")
		return ctrl.Result{}, nil
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
		if err := controllerutil.SetOwnerReference(pcs, actual, r.Scheme); err != nil {
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
		cond.Status = metav1.ConditionUnknown
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

func (r *PerconaServerMySQLClusterSetReconciler) bootstrapClusterSet(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) (bool, error) {
	return false, nil
}

func (r *PerconaServerMySQLClusterSetReconciler) configureReplicas(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) (bool, error) {
	return false, nil
}

func (r *PerconaServerMySQLClusterSetReconciler) reconcileSwitchover(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) error {
	return nil
}

func (r *PerconaServerMySQLClusterSetReconciler) reconcileStatus(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) error {
	return nil
}
