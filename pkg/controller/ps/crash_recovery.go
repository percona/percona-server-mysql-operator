package ps

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"
	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
)

var (
	noSuchFile         = errors.New("no such file or directory")
	noFullClusterCrash = errors.New("no full cluster crash")
)

func (r *PerconaServerMySQLReconciler) reconcileFullClusterCrash(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
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

	// we need every pod to be ready to reboot
	for _, pod := range pods {
		if !k8s.IsPodReady(pod) {
			return nil
		}
	}

	pod, gtids, err := r.findMaxSeqPod(ctx, pods)
	if err != nil {
		if errors.Is(err, noFullClusterCrash) {
			return nil
		}
		return errors.Wrap(err, "find max seq pod")
	}

	r.Recorder.Event(cr, "Warning", "FullClusterCrashDetected", "Pods can't join the cluster")
	log.Info("Full cluster crash recovery is needed!")

	if !cr.Spec.MySQL.AutoRecovery {
		fullClusterCrashMessage := ("Full cluster crash detected but auto recovery is not enabled." +
			"Enable .spec.mysql.autoRecovery or recover cluster manually." +
			"To recover manually you need connect to the pod that you want to" +
			"reboot cluster from and run 'dba.rebootClusterFromCompleteOutage() using" +
			"mysqlsh. Then delete /var/lib/mysql/full-cluster-crash in each pod." +
			"Here are GTID_EXECUTED values from each pod:")

		for pod, gtid := range gtids {
			fullClusterCrashMessage += fmt.Sprintf(" %s:%s", pod, gtid)
		}

		log.Error(nil, fullClusterCrashMessage)
		return nil
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	podFQDN := fmt.Sprintf("%s.%s.%s", pod.Name, mysql.ServiceName(cr), cr.Namespace)
	podUri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, podFQDN)

	mysh, err := mysqlsh.NewWithExec(r.ClientCmd, &pod, podUri)
	if err != nil {
		return err
	}

	status, err := mysh.ClusterStatusWithExec(ctx, cr.InnoDBClusterName())
	if err == nil && status.DefaultReplicaSet.Status == innodbcluster.ClusterStatusOK {
		log.Info("Cluster seems healthy, stopping recovery.")

		err := r.cleanupFullClusterCrashFile(ctx, cr)
		if err != nil {
			return errors.Wrap(err, "clean up full cluster crash file")
		}

		return nil
	}

	log.Info("Attempting to reboot cluster from complete outage")
	if err := mysh.RebootClusterFromCompleteOutageWithExec(ctx, cr.InnoDBClusterName()); err != nil {
		if strings.Contains(err.Error(), "The Cluster is ONLINE") {
			log.Info("Tried to reboot the cluster but MySQL says the cluster is already online. Deleting all MySQL pods")

			err := r.Client.DeleteAllOf(ctx, &corev1.Pod{}, &client.DeleteAllOfOptions{
				ListOptions: client.ListOptions{
					LabelSelector: labels.SelectorFromSet(mysql.MatchLabels(cr)),
					Namespace:     cr.Namespace,
				},
			})
			if err != nil {
				return errors.Wrap(err, "failed to delete MySQL pods")
			}

			return nil
		}

		r.Recorder.Event(cr, "Error", "FullClusterCrashRecoveryFailed", err.Error())
		return errors.Wrap(err, "reboot cluster")
	}

	log.Info("Cluster was successfully rebooted")
	r.Recorder.Event(cr, "Normal", "FullClusterCrashRecovered", "Cluster recovered from full cluster crash")

	if err := r.cleanupFullClusterCrashFile(ctx, cr); err != nil {
		return errors.Wrap(err, "clean up full cluster crash file")
	}

	return nil
}

// getLastSeq returns latest sequence number in GTID_EXECUTED
func getLastSeq(gtidExecuted string) (int64, error) {
	var seq int64

	splitted := strings.Split(gtidExecuted, ",")
	if len(splitted) < 1 {
		return seq, errors.Errorf("invalid gtid_executed: %s", gtidExecuted)
	}

	// get the last gtid set if there are multiple
	gtid := splitted[len(splitted)-1]

	g := strings.Split(gtid, ":")
	if len(g) != 2 {
		return seq, errors.Errorf("invalid gtid: %s", gtid)
	}

	if strings.Contains(g[1], "-") {
		g = strings.Split(g[1], "-")
	}

	seq, err := strconv.ParseInt(g[1], 10, 64)
	if err != nil {
		return seq, errors.Wrapf(err, "parse sequence %s", g[1])
	}

	return seq, nil

}

func (r *PerconaServerMySQLReconciler) getGtidExecuted(ctx context.Context, pod *corev1.Pod) (gomysql.GTIDSet, error) {
	var outb, errb bytes.Buffer

	cmd := []string{"cat", "/var/lib/mysql/full-cluster-crash"}

	err := r.ClientCmd.Exec(ctx, pod, "mysql", cmd, nil, &outb, &errb, false)
	if err != nil {
		if strings.Contains(errb.String(), "No such file or directory") {
			return nil, noSuchFile
		}
		return nil, errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, outb.String(), errb.String())
	}

	gtidSet, err := gomysql.ParseMysqlGTIDSet(outb.String())
	if err != nil {
		return nil, errors.Wrap(err, "parse GTID set")
	}

	return gtidSet, nil
}

// findHighestSequence finds the pod with highest sequence in its GTID_EXECUTED
func (r *PerconaServerMySQLReconciler) findMaxSeqPod(
	ctx context.Context,
	pods []corev1.Pod,
) (corev1.Pod, map[string]gomysql.GTIDSet, error) {
	gtids := make(map[string]gomysql.GTIDSet)

	var maxSeqPod corev1.Pod

	for _, pod := range pods {
		gtidExecuted, err := r.getGtidExecuted(ctx, &pod)
		if err != nil {
			if errors.Is(err, noSuchFile) {
				continue
			}

			return pod, nil, errors.Wrapf(err, "get gtid executed from %s", pod.Name)
		}

		gtids[pod.Name] = gtidExecuted
	}

	if len(gtids) < 1 {
		return maxSeqPod, gtids, noFullClusterCrash
	}

	return maxSeqPod, gtids, nil
}

func (r *PerconaServerMySQLReconciler) cleanupFullClusterCrashFile(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx)

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr), cr.Namespace)
	if err != nil {
		return errors.Wrap(err, "get pods")
	}

	var outb, errb bytes.Buffer
	cmd := []string{"rm", "/var/lib/mysql/full-cluster-crash"}
	for _, pod := range pods {
		err = r.ClientCmd.Exec(ctx, &pod, "mysql", cmd, nil, &outb, &errb, false)
		if err != nil {
			if strings.Contains(errb.String(), "No such file or directory") {
				continue
			}
			return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, outb.String(), errb.String())
		}
		log.V(1).Info("Removed /var/lib/mysql/full-cluster-crash", "pod", pod.Name)
	}

	return nil
}
