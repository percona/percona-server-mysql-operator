package ps

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	k8sretry "k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

const controllerRevisionHash = "controller-revision-hash"

func (r *PerconaServerMySQLReconciler) smartUpdate(ctx context.Context, sts *appsv1.StatefulSet, cr *apiv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("SmartUpdate")

	if cr.Spec.Pause {
		return nil
	}

	currentSet := sts
	err := r.Client.Get(ctx, types.NamespacedName{
		Name:      sts.Name,
		Namespace: sts.Namespace,
	}, currentSet)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return errors.Wrap(err, "failed to get current sts")
	}

	pods := corev1.PodList{}
	if err := r.Client.List(ctx, &pods, &client.ListOptions{
		Namespace:     currentSet.Namespace,
		LabelSelector: labels.SelectorFromSet(currentSet.Spec.Selector.MatchLabels),
	}); err != nil {
		return errors.Wrap(err, "get pod list")
	}

	if !stsChanged(currentSet, pods.Items) {
		return nil
	}

	log.Info("statefulSet was changed, run smart update")

	running, err := r.isBackupRunning(ctx, cr)
	if err != nil {
		log.Error(err, "can't start 'SmartUpdate'")
		return nil
	}
	if running {
		log.Info("can't start/continue 'SmartUpdate': backup is running")
		return nil
	}

	if currentSet.Status.ReadyReplicas < currentSet.Status.Replicas {
		log.Info("Can't start/continue 'SmartUpdate': waiting for all replicas to be ready")
		return nil
	}

	primaryHost, err := r.getPrimaryHost(ctx, cr)
	if err != nil {
		return err
	}
	idx, err := getPodIndexFromHostname(primaryHost)
	if err != nil {
		return err
	}
	primPod, err := mysql.GetPod(ctx, r.Client, cr, idx)
	if err != nil {
		return errors.Wrap(err, "get primary pod")
	}
	log.Info("primary pod", "name", primPod.Name)

	secondaries := slices.DeleteFunc(pods.Items, func(p corev1.Pod) bool {
		return p.Name == primPod.Name
	})

	for _, pod := range secondaries {
		pod := pod

		log.Info("apply changes to the secondary pod", "pod", pod.Name)

		if pod.ObjectMeta.Labels[controllerRevisionHash] == sts.Status.UpdateRevision {
			log.Info("pod updated", "pod", pod.Name)
			continue
		}

		return deletePodAndWait(ctx, r.Client, &pod, currentSet)
	}

	target, err := selectPrimaryCandidate(secondaries)
	if err != nil {
		return errors.Wrap(err, "select primary candidate")
	}

	if err := r.switchOverAndWait(logf.IntoContext(ctx, log), cr, primPod, target); err != nil {
		return errors.Wrap(err, "switchover")
	}

	log.Info("apply changes to the primary pod", "pod", primPod.Name)
	if primPod.ObjectMeta.Labels[controllerRevisionHash] != sts.Status.UpdateRevision {
		log.Info("primary pod was deleted", "pod", primPod.Name)
		err = deletePodAndWait(ctx, r.Client, primPod, currentSet)
		if err != nil {
			log.Info("primary pod deletion error", "pod", primPod.Name)
			return err
		}
	}

	log.Info("primary pod updated", "pod", primPod.Name)
	log.Info("smart update finished")
	return nil
}

func stsChanged(sts *appsv1.StatefulSet, pods []corev1.Pod) bool {
	// When https://github.com/kubernetes/kubernetes/issues/73492 bug gets fixed,
	// we can simply compare sts.Status.UpdateRevision with sts.Status.CurrentRevision
	for _, pod := range pods {
		if pod.ObjectMeta.Labels["controller-revision-hash"] != sts.Status.UpdateRevision {
			return true
		}
	}

	return false
}

func (r *PerconaServerMySQLReconciler) isBackupRunning(ctx context.Context, cr *apiv1.PerconaServerMySQL) (bool, error) {
	bcpList := apiv1.PerconaServerMySQLBackupList{}
	if err := r.Client.List(ctx, &bcpList, &client.ListOptions{Namespace: cr.Namespace}); err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}
		return false, errors.Wrap(err, "failed to get backup object")
	}

	for _, bcp := range bcpList.Items {
		if bcp.Spec.ClusterName != cr.Name {
			continue
		}

		if bcp.Status.State == apiv1.BackupRunning || bcp.Status.State == apiv1.BackupStarting {
			return true, nil
		}
	}

	return false, nil
}

func selectPrimaryCandidate(pods []corev1.Pod) (*corev1.Pod, error) {
	for i := range pods {
		if k8s.IsPodReady(pods[i]) {
			return &pods[i], nil
		}
	}

	return nil, errors.New("no ready secondaries")
}

func (r *PerconaServerMySQLReconciler) switchOverAndWait(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	primary *corev1.Pod, target *corev1.Pod,
) error {
	log := logf.FromContext(ctx)

	log.Info("switchover", "current", primary.Name, "target", target.Name)

	switch {
	case cr.MySQLSpec().IsAsync():
		err := r.switchOverAsync(ctx, cr, primary, target)
		if err != nil {
			return errors.Wrap(err, "switchover async")
		}
	case cr.MySQLSpec().IsGR():
		err := r.switchOverGR(ctx, cr, primary, target)
		if err != nil {
			return errors.Wrap(err, "switchover group-replication")
		}
	}

	retry := wait.Backoff{
		Duration: 3 * time.Second,
		Steps:    10,
		Factor:   1.0,
		Jitter:   0.1,
	}
	errPrimaryNotChanged := errors.New("primary not changed")
	err := k8sretry.OnError(retry, func(err error) bool {
		return errors.Is(err, errPrimaryNotChanged)
	}, func() error {
		primHost, err := r.getPrimaryHost(ctx, cr)
		if err != nil {
			return err
		}

		if !strings.HasPrefix(primHost, target.Name) {
			return errPrimaryNotChanged
		}

		return nil
	})
	if err != nil {
		return errors.Wrap(err, "wait for new primary")
	}
	log.Info("target is primary", "target", target.Name)

	// in async clusters primary is labelled by orchestrator
	if cr.MySQLSpec().IsGR() {
		if err := r.reconcileGRMySQLPrimaryLabel(ctx, cr); err != nil {
			return errors.Wrap(err, "reconcile primary label")
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) switchOverAsync(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	primary *corev1.Pod, target *corev1.Pod,
) error {
	orcPod, err := getReadyOrcPod(ctx, r.Client, cr)
	if err != nil {
		return errors.Wrap(err, "get ready orchestrator pod")
	}

	err = orchestrator.EnsureNodeIsPrimary(ctx, r.ClientCmd, orcPod, cr.ClusterHint(), target.GetName(), mysql.DefaultPort)
	if err != nil {
		return errors.Wrap(err, "ensure node is primary")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) switchOverGR(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	primary *corev1.Pod, target *corev1.Pod,
) error {
	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	primaryUri := getMySQLURI(apiv1.UserOperator, operatorPass, mysql.PodFQDN(cr, primary))
	mysh, err := mysqlsh.NewWithExec(r.ClientCmd, primary, primaryUri)
	if err != nil {
		return err
	}

	targetFQDN := mysql.PodFQDN(cr, target)
	if err := mysh.SetPrimaryInstanceWithExec(ctx, cr.InnoDBClusterName(), targetFQDN); err != nil {
		return errors.Wrap(err, "set primary instance")
	}

	return nil
}

// deletePodAndWait deletes the pod and waits for pod to be updated and get ready
func deletePodAndWait(ctx context.Context, cli client.Client, pod *corev1.Pod, sts *appsv1.StatefulSet) error {
	err := cli.Delete(ctx, pod)
	if err != nil {
		return err
	}

	retriable := func(err error) bool {
		return err != nil
	}

	retry := wait.Backoff{
		Duration: 10 * time.Second,
		Steps:    15,
		Factor:   1.0,
		Jitter:   0.1,
	}

	return k8sretry.OnError(retry, retriable, func() error {
		p := &corev1.Pod{}
		err := cli.Get(ctx, types.NamespacedName{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		}, p)
		if err != nil {
			return errors.Wrap(err, "failed to get pod")
		}

		if p.ObjectMeta.Labels[controllerRevisionHash] != sts.Status.UpdateRevision {
			return errors.New("pod is not updated")
		}

		if !k8s.IsPodReady(*p) {
			return errors.New("pod is not ready")
		}

		return nil
	})
}
