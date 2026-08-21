package ps

import (
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

const fullClusterCrashFile = "/var/lib/mysql/full-cluster-crash"

type fullClusterCrashPods struct {
	marked    []corev1.Pod // pods that cannot connect and suspect a full cluster crash
	witnesses []corev1.Pod // pods that are online and can witness the cluster status
}

func (r *PerconaServerMySQLReconciler) reconcileFullClusterCrash(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("Crash recovery")

	if cr.Spec.MySQL.IsAsync() {
		return nil
	}

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr), cr.Namespace)
	if err != nil {
		return errors.Wrap(err, "get pods")
	}

	if len(pods) < int(cr.MySQLSpec().Size) {
		return nil
	}

	if !allPodsReady(pods) {
		return nil
	}

	crashPods, err := r.getFullClusterCrashPods(ctx, pods)
	if err != nil {
		return err
	}
	if len(crashPods.marked) == 0 {
		return nil
	}

	if !cr.Spec.MySQL.AutoRecovery {
		log.Error(nil, `
			Full cluster crash detected but auto recovery is not enabled.
			Enable .spec.mysql.autoRecovery or recover cluster manually
			(connect to one of the pods using mysql-shell and run 'dba.rebootClusterFromCompleteOutage() and delete /var/lib/mysql/full-cluster-crash in each pod.').`)
		return nil
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	online, err := r.clusterOnlineFromPods(ctx, cr, crashPods.witnesses, operatorPass)
	if err != nil {
		return err
	}
	if online {
		log.Info("Cluster is online, restarting crashed members")
		r.restartStaleFullClusterCrashPods(ctx, crashPods.marked)
		return nil
	}

	for _, pod := range crashPods.marked {
		mysh, podFQDN, err := r.mysqlShellForPod(cr, &pod, operatorPass)
		if err != nil {
			return err
		}

		log.Info("Attempting to reboot cluster from complete outage", "pod", pod.Name, "host", podFQDN)
		err = mysh.RebootClusterFromCompleteOutageWithExec(ctx, cr.InnoDBClusterName())
		if err == nil {
			r.finishFullClusterCrashRecovery(ctx, cr, &pod, podFQDN)
			break
		}

		if strings.Contains(err.Error(), "The Cluster is ONLINE") {
			log.Info("Tried to reboot the cluster but MySQL says the cluster is already online. Deleting pods with stale full cluster crash markers.")
			r.restartStaleFullClusterCrashPods(ctx, crashPods.marked)
			break
		}

		log.Error(err, "failed to reboot cluster from complete outage", "pod", pod.Name, "host", podFQDN)
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) finishFullClusterCrashRecovery(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	recoveryPod *corev1.Pod,
	recoveryHost string,
) {
	log := logf.FromContext(ctx).WithName("Crash recovery")

	log.Info("Cluster was successfully rebooted", "pod", recoveryPod.Name, "host", recoveryHost)
	r.Recorder.Event(cr, corev1.EventTypeNormal, "FullClusterCrashRecovered", "Cluster recovered from full cluster crash")

	if err := r.cleanupFullClusterCrashFile(ctx, cr); err != nil {
		log.Error(err, "failed to remove /var/lib/mysql/full-cluster-crash")
		return
	}

	primary, err := r.primaryPodAfterFullClusterCrash(ctx, cr)
	if err != nil {
		log.Error(err, "failed to get primary pod")
		return
	}

	log.Info("Primary pod detected", "pod", primary.Name)
	r.deleteSecondaryPods(ctx, cr, primary.Name)
}

func (r *PerconaServerMySQLReconciler) primaryPodAfterFullClusterCrash(ctx context.Context, cr *apiv1.PerconaServerMySQL) (*corev1.Pod, error) {
	var primary *corev1.Pod

	err := retry.OnError(wait.Backoff{
		Duration: 10 * time.Second,
		Factor:   1.5,
		Steps:    10,
	}, func(err error) bool { return true }, func() error {
		var err error

		primary, err = r.getPrimaryPod(ctx, cr)
		if err != nil {
			logf.FromContext(ctx).WithName("Crash recovery").V(1).Error(err, "failed to get primary pod")
			return err
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return primary, nil
}

func (r *PerconaServerMySQLReconciler) deleteSecondaryPods(ctx context.Context, cr *apiv1.PerconaServerMySQL, primaryPodName string) {
	log := logf.FromContext(ctx).WithName("Crash recovery")

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr), cr.Namespace)
	if err != nil {
		log.Error(err, "failed to get mysql pods")
		return
	}

	for _, pod := range pods {
		if pod.Name == primaryPodName {
			continue
		}

		log.Info("Deleting secondary pod", "pod", pod.Name)
		if err := r.Delete(ctx, &pod); err != nil {
			log.Error(err, "failed to delete pod", "pod", pod.Name)
		}
	}
}

func allPodsReady(pods []corev1.Pod) bool {
	for _, pod := range pods {
		if !k8s.IsPodReady(pod) {
			return false
		}
	}
	return true
}

func (r *PerconaServerMySQLReconciler) getFullClusterCrashPods(ctx context.Context, pods []corev1.Pod) (fullClusterCrashPods, error) {
	log := logf.FromContext(ctx).WithName("Crash recovery")
	crashPods := fullClusterCrashPods{}

	for _, pod := range pods {
		if ok, err := r.hasClusterSetRecoveryFile(ctx, &pod); err != nil {
			return crashPods, err
		} else if ok {
			return fullClusterCrashPods{}, nil
		}

		gtidExecuted, ok, err := r.fullClusterCrashGTID(ctx, &pod)
		if err != nil {
			return crashPods, err
		}
		if !ok {
			crashPods.witnesses = append(crashPods.witnesses, pod)
			continue
		}

		log.Info("Pod is waiting for recovery", "pod", pod.Name, "gtidExecuted", gtidExecuted)
		crashPods.marked = append(crashPods.marked, pod)
	}

	return crashPods, nil
}

func (r *PerconaServerMySQLReconciler) hasClusterSetRecoveryFile(ctx context.Context, pod *corev1.Pod) (bool, error) {
	_, exists, err := r.readFileInMySQLPod(ctx, pod, naming.ClusterSetRecoveryFile)
	if err != nil {
		return false, errors.Wrapf(err, "check clusterset recovery file")
	}
	return exists, nil
}

func (r *PerconaServerMySQLReconciler) fullClusterCrashGTID(ctx context.Context, pod *corev1.Pod) (string, bool, error) {
	return r.readFileInMySQLPod(ctx, pod, fullClusterCrashFile)
}

func (r *PerconaServerMySQLReconciler) readFileInMySQLPod(ctx context.Context, pod *corev1.Pod, file string) (string, bool, error) {
	var outb, errb bytes.Buffer
	cmd := []string{"cat", file}

	err := r.ClientCmd.Exec(ctx, pod, "mysql", cmd, nil, &outb, &errb, false)
	if err != nil {
		if strings.Contains(errb.String(), "No such file or directory") {
			return "", false, nil
		}
		return "", false, errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, outb.String(), errb.String())
	}

	return outb.String(), true, nil
}

func (r *PerconaServerMySQLReconciler) clusterOnlineFromPods(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	pods []corev1.Pod,
	operatorPass string,
) (bool, error) {
	log := logf.FromContext(ctx).WithName("Crash recovery")

	checkPod := func(pod *corev1.Pod) (bool, error) {
		mysh, podFQDN, err := r.mysqlShellForPod(cr, pod, operatorPass)
		if err != nil {
			return false, errors.Wrapf(err, "create mysqlsh client for pod %s", pod.Name)
		}

		status, err := mysh.ClusterStatusWithExec(ctx)
		if err != nil {
			log.V(1).Error(err, "failed to get cluster status", "pod", pod.Name, "host", podFQDN)
			return false, nil
		}

		if status.DefaultReplicaSet.Status.IsOnline() {
			log.Info("Cluster is online", "pod", pod.Name, "host", podFQDN, "status", status.DefaultReplicaSet.Status)
			return true, nil
		}

		log.V(1).Info("Cluster is not online", "pod", pod.Name, "host", podFQDN, "status", status.DefaultReplicaSet.Status)
		return false, nil
	}

	for i := range pods {
		online, err := checkPod(&pods[i])
		if online || err != nil {
			return online, err
		}
	}

	return false, nil
}

func (r *PerconaServerMySQLReconciler) restartStaleFullClusterCrashPods(ctx context.Context, markerPods []corev1.Pod) {
	log := logf.FromContext(ctx).WithName("Crash recovery")

	for _, pod := range markerPods {
		removed, err := r.removeFullClusterCrashFileFromPod(ctx, &pod)
		if err != nil {
			log.Error(err, "failed to remove /var/lib/mysql/full-cluster-crash", "pod", pod.Name)
			continue
		}
		if !removed {
			log.Info("Full cluster crash marker is already removed", "pod", pod.Name)
			continue
		}

		log.Info("Deleting pod with stale full cluster crash marker", "pod", pod.Name)
		if err := r.Delete(ctx, &pod); err != nil {
			log.Error(err, "failed to delete pod", "pod", pod.Name)
		}
	}
}

func (r *PerconaServerMySQLReconciler) mysqlShellForPod(
	cr *apiv1.PerconaServerMySQL,
	pod *corev1.Pod,
	operatorPass string,
) (*mysqlsh.MysqlshExec, string, error) {
	podFQDN := mysql.PodFQDN(cr, pod)
	podUri := mysqlsh.URI(string(apiv1.UserOperator), operatorPass, podFQDN)

	opts := &mysqlsh.ExecOptions{
		Pod:           pod,
		ContainerName: "mysql",
		Client:        r.ClientCmd,
		Stdout:        &bytes.Buffer{},
	}
	mysh, err := mysqlsh.NewWithExec(podUri, opts)
	if err != nil {
		return nil, podFQDN, err
	}

	return mysh, podFQDN, nil
}

func (r *PerconaServerMySQLReconciler) removeFullClusterCrashFileFromPod(ctx context.Context, pod *corev1.Pod) (bool, error) {
	var outb, errb bytes.Buffer
	cmd := []string{"rm", fullClusterCrashFile}

	err := r.ClientCmd.Exec(ctx, pod, "mysql", cmd, nil, &outb, &errb, false)
	if err != nil {
		if strings.Contains(errb.String(), "No such file or directory") {
			return false, nil
		}
		return false, errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, outb.String(), errb.String())
	}

	return true, nil
}

func (r *PerconaServerMySQLReconciler) cleanupFullClusterCrashFile(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx)

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr), cr.Namespace)
	if err != nil {
		return errors.Wrap(err, "get pods")
	}

	for _, pod := range pods {
		removed, err := r.removeFullClusterCrashFileFromPod(ctx, &pod)
		if err != nil {
			return err
		}
		if !removed {
			continue
		}
		log.V(1).Info("Removed /var/lib/mysql/full-cluster-crash", "pod", pod.Name)
	}

	return nil
}
