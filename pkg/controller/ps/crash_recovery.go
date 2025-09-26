package ps

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
)

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

	// we need every pod to be ready to reboot
	for _, pod := range pods {
		if !k8s.IsPodReady(pod) {
			return nil
		}
	}

	var outb, errb bytes.Buffer
	cmd := []string{"cat", "/var/lib/mysql/full-cluster-crash"}

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

		if !k8s.IsPodReady(pod) {
			continue
		}

		operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1.UserOperator)
		if err != nil {
			return errors.Wrap(err, "get operator password")
		}

		podFQDN := mysql.PodFQDN(cr, &pod)
		podUri := getMySQLURI(apiv1.UserOperator, operatorPass, podFQDN)

		mysh, err := mysqlsh.NewWithExec(r.ClientCmd, &pod, podUri)
		if err != nil {
			return err
		}

		status, err := mysh.ClusterStatusWithExec(ctx)
		if err == nil && status.DefaultReplicaSet.Status == innodbcluster.ClusterStatusOK {
			log.Info("Cluster is healthy", "pod", pod.Name, "host", podFQDN)
			err := r.cleanupFullClusterCrashFile(ctx, cr)
			if err != nil {
				log.Error(err, "failed to remove /var/lib/mysql/full-cluster-crash")
			}
			continue
		}

		log.Info("Attempting to reboot cluster from complete outage", "pod", pod.Name, "host", podFQDN)
		err = mysh.RebootClusterFromCompleteOutageWithExec(ctx, cr.InnoDBClusterName())
		if err == nil {
			log.Info("Cluster was successfully rebooted", "pod", pod.Name, "host", podFQDN)
			r.Recorder.Event(cr, corev1.EventTypeNormal, "FullClusterCrashRecovered", "Cluster recovered from full cluster crash")
			err := r.cleanupFullClusterCrashFile(ctx, cr)
			if err != nil {
				log.Error(err, "failed to remove /var/lib/mysql/full-cluster-crash")
				break
			}

			var primary *corev1.Pod
			err = retry.OnError(wait.Backoff{
				Duration: 10 * time.Second,
				Factor:   1.5,
				Steps:    10,
			}, func(err error) bool { return true }, func() error {
				var err error

				primary, err = r.getPrimaryPod(ctx, cr)
				if err != nil {
					log.V(1).Error(err, "failed to get primary pod")
					return err
				}

				return nil
			})
			if err != nil {
				log.Error(err, "failed to get primary pod")
				break
			}

			log.Info(fmt.Sprintf("Primary pod is %s", primary.Name))

			pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr), cr.Namespace)
			if err != nil {
				log.Error(err, "failed to get mysql pods")
				break
			}

			for _, pod := range pods {
				if pod.Name == primary.Name {
					continue
				}

				log.Info("Deleting secondary pod", "pod", pod.Name)
				if err := r.Delete(ctx, &pod); err != nil {
					log.Error(err, "failed to delete pod", "pod", pod.Name)
				}
			}

			break
		}

		// TODO: This needs to reconsidered.
		if strings.Contains(err.Error(), "The Cluster is ONLINE") {
			log.Info("Tried to reboot the cluster but MySQL says the cluster is already online. Deleting all MySQL pods.")
			err := r.DeleteAllOf(ctx, &corev1.Pod{}, &client.DeleteAllOfOptions{
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

		log.Error(err, "failed to reboot cluster from complete outage", "pod", pod.Name, "host", podFQDN)
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) cleanupFullClusterCrashFile(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
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
