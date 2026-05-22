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
	"k8s.io/apimachinery/pkg/labels"
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
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
)

type PerconaServerMySQLClusterSetReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	ClientCmd clientcmd.Client
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

	shell, err := r.newMySQLShell(ctx, pcs)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "new mysqlsh")
	}

	if err := r.bootstrapClusterSet(ctx, pcs, shell); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "bootstrap ClusterSet")
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

func (r *PerconaServerMySQLClusterSetReconciler) bootstrapClusterSet(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet, shell *mysqlsh.MysqlshExec) error {
	// Already bootstrapped, early return.
	if meta.IsStatusConditionTrue(pcs.Status.Conditions, apiv1.ConditionClusterSetBootstrapped) {
		return nil
	}

	return nil
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

func (r *PerconaServerMySQLClusterSetReconciler) getRunnerPod(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) (*corev1.Pod, error) {
	selector := clusterset.MySQLShellRunner(pcs).Spec.Selector.MatchLabels
	listOptions := &client.ListOptions{
		Namespace:     pcs.Namespace,
		LabelSelector: labels.SelectorFromSet(selector),
	}

	runnerPods := &corev1.PodList{}
	if err := r.List(ctx, runnerPods, listOptions); err != nil {
		return nil, errors.Wrap(err, "list runner pods")
	}

	if len(runnerPods.Items) == 0 {
		return nil, errors.New("no runner pods found")
	}

	runnerPod := runnerPods.Items[0]
	return &runnerPod, nil
}

func (r *PerconaServerMySQLClusterSetReconciler) getClustersetAdminPassword(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) (string, error) {
	secret := &corev1.Secret{}
	secretKeySel := pcs.Spec.CredentialsSecret
	if err := r.Get(ctx, client.ObjectKey{Namespace: pcs.Namespace, Name: secretKeySel.Name}, secret); err != nil {
		return "", errors.Wrap(err, "get credentials secret")
	}

	password, ok := secret.Data[string(secretKeySel.Key)]
	if !ok {
		return "", errors.New("no password for clusterset admin found")
	}

	return string(password), nil
}

func (r *PerconaServerMySQLClusterSetReconciler) newMySQLShell(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet) (*mysqlsh.MysqlshExec, error) {
	runnerPod, err := r.getRunnerPod(ctx, pcs)
	if err != nil {
		return nil, errors.Wrap(err, "get runner pod")
	}

	pass, err := r.getClustersetAdminPassword(ctx, pcs)
	if err != nil {
		return nil, errors.Wrap(err, "get clusterset admin password")
	}

	primaryCluster := pcs.PrimaryCluster()
	if primaryCluster == nil {
		return nil, errors.New("primary cluster not found")
	}

	// TODO: use clusterAdmin here, not root
	// TODO: use any endpoint, not just first one
	primaryClusterURI := mysqlsh.URI(string(apiv1.UserRoot), pass, primaryCluster.Endpoints[0].Host)

	shell, err := mysqlsh.NewWithExec(r.ClientCmd, runnerPod, primaryClusterURI)
	if err != nil {
		return nil, errors.Wrap(err, "new mysqlsh")
	}

	return shell, nil
}
