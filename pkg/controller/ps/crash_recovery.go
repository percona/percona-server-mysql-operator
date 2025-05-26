package ps

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
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

	var outb, errb bytes.Buffer
	cmd := []string{"cat", "/var/lib/mysql/full-cluster-crash"}

	clusterCrashRecovered := false
	for _, pod := range pods {
		err = r.ClientCmd.Exec(ctx, &pod, "mysql", cmd, nil, &outb, &errb, false)
		if err != nil {
			if strings.Contains(errb.String(), "No such file or directory") {
				continue
			}
			return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, outb.String(), errb.String())
		}

		log.Info("Pod is waiting for recovery", "pod", pod.Name, "gtidExecuted", outb.String())

		if !cr.Spec.MySQL.AutoRecovery {
			log.Error(nil, `
			Full cluster crash detected but auto recovery is not enabled.
			Enable .spec.mysql.autoRecovery or recover cluster manually
			(connect to one of the pods using mysql-shell and run 'dba.rebootClusterFromCompleteOutage() and delete /var/lib/mysql/full-cluster-crash in each pod.').`)
			continue
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
			err := r.cleanupFullClusterCrashFile(ctx, cr, &pod)
			if err != nil {
				log.Error(err, "failed to remove /var/lib/mysql/full-cluster-crash")
			}
			continue
		}

		log.Info("Attempting to reboot cluster from complete outage")
		err = mysh.RebootClusterFromCompleteOutageWithExec(ctx, cr.InnoDBClusterName())
		if err == nil {
			log.Info("Cluster was successfully rebooted")
			clusterCrashRecovered = true
			err := r.cleanupFullClusterCrashFile(ctx, cr, &pod)
			if err != nil {
				log.Error(err, "failed to remove /var/lib/mysql/full-cluster-crash")
			}
			break
		}

		if strings.Contains(err.Error(), "The Cluster is ONLINE") {
			log.Info("Tried to reboot the cluster but MySQL says the cluster is already online")
			log.Info("Deleting all MySQL pods")
			err := r.Client.DeleteAllOf(ctx, &corev1.Pod{}, &client.DeleteAllOfOptions{
				ListOptions: client.ListOptions{
					LabelSelector: labels.SelectorFromSet(mysql.MatchLabels(cr)),
					Namespace:     cr.Namespace,
				},
			})
			if err != nil {
				return errors.Wrap(err, "failed to delete MySQL pods")
			}
			break
		}

		log.Error(err, "failed to reboot cluster from complete outage")
	}
	if clusterCrashRecovered {
		r.Recorder.Event(cr, "Normal", "FullClusterCrashRecovered", "Cluster recovered from full cluster crash")
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) cleanupFullClusterCrashFile(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, pod *corev1.Pod) error {
	log := logf.FromContext(ctx)

	var outb, errb bytes.Buffer
	cmd := []string{"rm", "/var/lib/mysql/full-cluster-crash"}
	err := r.ClientCmd.Exec(ctx, pod, "mysql", cmd, nil, &outb, &errb, false)
	if err != nil {
		if strings.Contains(errb.String(), "No such file or directory") {
			return nil
		}
		return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, outb.String(), errb.String())
	}
	log.V(1).Info("Removed /var/lib/mysql/full-cluster-crash", "pod", pod.Name)

	return nil
}
