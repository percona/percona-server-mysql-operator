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
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

const controllerRevisionHash = "controller-revision-hash"

var errPrimaryNotTheLowest = errors.New("appointed primary member is not the lowest version in the group")

func (r *PerconaServerMySQLReconciler) smartUpdate(ctx context.Context, sts *appsv1.StatefulSet, cr *apiv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("SmartUpdate").WithValues("sts", sts.Name)

	if cr.Spec.Pause {
		return nil
	}

	currentSet := new(appsv1.StatefulSet)
	if err := r.Get(ctx, client.ObjectKeyFromObject(sts), currentSet); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return errors.Wrap(err, "failed to get current sts")
	}

	pods := corev1.PodList{}
	if err := r.List(ctx, &pods, &client.ListOptions{
		Namespace:     currentSet.Namespace,
		LabelSelector: labels.SelectorFromSet(currentSet.Spec.Selector.MatchLabels),
	}); err != nil {
		return errors.Wrap(err, "get pod list")
	}

	if !stsChanged(currentSet, pods.Items) {
		return nil
	}

	log.Info("statefulSet was changed, run smart update")

	component := sts.Labels[naming.LabelComponent]

	if component == naming.ComponentDatabase {
		running, err := r.isBackupRunning(ctx, cr)
		if err != nil {
			log.Error(err, "can't start 'SmartUpdate'")
			return nil
		}
		if running {
			log.Info("can't start/continue 'SmartUpdate': backup is running")
			return nil
		}
	}

	if currentSet.Status.ReadyReplicas < currentSet.Status.Replicas {
		err := deleteOutdatedStuckPods(ctx, r.Client, pods.Items, currentSet.Status.UpdateRevision)
		if err != nil {
			return errors.Wrap(err, "delete outdated unready pods")
		}

		log.Info("Can't start/continue 'SmartUpdate': waiting for all replicas to be ready")
		return nil
	}

	var last *corev1.Pod
	var err error
	switch component {
	case naming.ComponentDatabase:
		last, err = r.mysqlPrimaryPod(ctx, cr)
		if err != nil {
			return errors.Wrap(err, "get primary pod")
		}
	case naming.ComponentOrchestrator:
		last, err = orchestratorRaftLeader(ctx, r.ClientCmd, pods.Items)
		if err != nil {
			return errors.Wrap(err, "get raft leader")
		}
		if last == nil {
			log.Info("Can't start/continue 'SmartUpdate': the Raft leader is unknown")
			return nil
		}
	default:
		return errors.Errorf("smart update is not supported for component %q", component)
	}
	log.Info("pod to update last", "pod", last.Name)

	pod := podToUpdate(pods.Items, last.Name, currentSet.Status.UpdateRevision)
	if pod == nil {
		return nil
	}

	if pod.Name == last.Name && component == naming.ComponentDatabase {
		secondaries := slices.DeleteFunc(slices.Clone(pods.Items), func(p corev1.Pod) bool {
			return p.Name == last.Name
		})

		target, err := selectPrimaryCandidate(secondaries)
		if err != nil {
			return errors.Wrap(err, "select primary candidate")
		}

		if err := r.switchOverAndWait(ctx, cr, last, target); err != nil {
			return errors.Wrap(err, "switchover")
		}
	}

	log.Info("apply changes to pod", "pod", pod.Name)
	if err := deletePodAndWait(ctx, r.Client, pod, currentSet); err != nil {
		return errors.Wrapf(err, "delete pod %s", pod.Name)
	}

	log.Info("smart update finished")
	return nil
}

func (r *PerconaServerMySQLReconciler) mysqlPrimaryPod(ctx context.Context, cr *apiv1.PerconaServerMySQL) (*corev1.Pod, error) {
	primaryHost, err := r.getPrimaryHost(ctx, cr)
	if err != nil {
		return nil, errors.Wrap(err, "get primary host")
	}

	idx, err := getPodIndexFromHostname(primaryHost)
	if err != nil {
		return nil, errors.Wrap(err, "get pod index from hostname")
	}

	pod, err := mysql.GetPod(ctx, r.Client, cr, idx)
	if err != nil {
		return nil, errors.Wrap(err, "get primary pod")
	}

	return pod, nil
}

func orchestratorRaftLeader(ctx context.Context, cliCmd clientcmd.Client, pods []corev1.Pod) (*corev1.Pod, error) {
	log := logf.FromContext(ctx).WithName("SmartUpdate").WithName("orchestratorRaftLeader")

	var leader *corev1.Pod
	for _, pod := range pods {
		state, err := orchestrator.RaftState(ctx, cliCmd, &pod)
		if err != nil {
			log.V(1).Info("Failed to get Raft state", "pod", pod.Name, "error", err.Error())
			continue
		}
		if state != orchestrator.RaftStateLeader {
			continue
		}
		if leader != nil {
			return nil, errors.Errorf("multiple pods report being the Raft leader: %s and %s", leader.Name, pod.Name)
		}
		leader = &pod
	}

	return leader, nil
}

func podToUpdate(pods []corev1.Pod, lastName, updateRevision string) *corev1.Pod {
	var last *corev1.Pod
	for _, pod := range pods {
		if pod.Labels[controllerRevisionHash] == updateRevision {
			continue
		}
		if pod.Name != lastName {
			return &pod
		}
		last = &pod
	}

	return last
}

func stsChanged(sts *appsv1.StatefulSet, pods []corev1.Pod) bool {
	// When https://github.com/kubernetes/kubernetes/issues/73492 bug gets fixed,
	// we can simply compare sts.Status.UpdateRevision with sts.Status.CurrentRevision
	for _, pod := range pods {
		if pod.Labels[controllerRevisionHash] != sts.Status.UpdateRevision {
			return true
		}
	}

	return false
}

func deleteOutdatedStuckPods(ctx context.Context, cli client.Client, pods []corev1.Pod, updateRevision string) error {
	log := logf.FromContext(ctx)

	if updateRevision == "" {
		return nil
	}

	for _, pod := range pods {
		if (pod.Status.Phase != corev1.PodPending && !isPodInCrashLoopBackOff(pod)) ||
			pod.Labels[controllerRevisionHash] == updateRevision ||
			pod.DeletionTimestamp != nil {
			continue
		}

		log.Info("deleting outdated stuck pod", "pod", pod.Name)
		if err := cli.Delete(ctx, &pod); client.IgnoreNotFound(err) != nil {
			return errors.Wrapf(err, "delete pod %s", pod.Name)
		}
	}

	return nil
}

const crashLoopBackOffReason = "CrashLoopBackOff"

func isPodInCrashLoopBackOff(pod corev1.Pod) bool {
	for _, statuses := range [][]corev1.ContainerStatus{
		pod.Status.InitContainerStatuses,
		pod.Status.ContainerStatuses,
	} {
		for _, status := range statuses {
			if status.State.Waiting != nil && status.State.Waiting.Reason == crashLoopBackOffReason {
				return true
			}
		}
	}

	return false
}

func (r *PerconaServerMySQLReconciler) isBackupRunning(ctx context.Context, cr *apiv1.PerconaServerMySQL) (bool, error) {
	bcpList := apiv1.PerconaServerMySQLBackupList{}
	if err := r.List(ctx, &bcpList, &client.ListOptions{Namespace: cr.Namespace}); err != nil {
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
		err := r.switchOverAsync(ctx, cr, target)
		if err != nil {
			return errors.Wrap(err, "switchover async")
		}
	case cr.MySQLSpec().IsGR():
		err := r.switchOverGR(ctx, cr, primary, target)
		if err != nil {
			if errors.Is(err, errPrimaryNotTheLowest) {
				log.Info("MySQL version upgrade is in progress. Falling back to failover.")
				return nil
			}
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

		hostParts := strings.SplitN(primHost, ".", 2)
		if len(hostParts) == 0 || hostParts[0] != target.Name {
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
	target *corev1.Pod,
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

func isErrPrimaryNotTheLowest(err error) bool {
	errStrings := []string{
		"The appointed primary member is not the lowest version in the group.",                                         // 8.4
		"The appointed primary member has a version that is greater than the one of some of the members in the group.", // 8.0
	}
	for _, errStr := range errStrings {
		if strings.Contains(err.Error(), errStr) {
			return true
		}
	}

	return false
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
	mysh, err := mysqlsh.NewWithExec(primaryUri, &mysqlsh.ExecOptions{
		Pod:           primary,
		ContainerName: "mysql",
		Client:        r.ClientCmd,
	})
	if err != nil {
		return err
	}

	targetFQDN := mysql.PodFQDN(cr, target)
	if err := mysh.SetPrimaryInstanceWithExec(ctx, cr.InnoDBClusterName(), targetFQDN); err != nil {
		if isErrPrimaryNotTheLowest(err) {
			return errPrimaryNotTheLowest
		}
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

		if p.Labels[controllerRevisionHash] != sts.Status.UpdateRevision {
			return errors.New("pod is not updated")
		}

		if !k8s.IsPodReady(*p) {
			return errors.New("pod is not ready")
		}

		return nil
	})
}
