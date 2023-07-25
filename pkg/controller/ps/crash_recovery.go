package ps

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
)

func (r *PerconaServerMySQLReconciler) reconcileFullClusterCrash(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("Crash recovery")

	cli, err := clientcmd.NewClient()
	if err != nil {
		return err
	}

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr))
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

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	var outb, errb bytes.Buffer
	cmd := []string{"cat", "/var/lib/mysql/full-cluster-crash"}

	for _, pod := range pods {
		err = cli.Exec(ctx, &pod, "mysql", cmd, nil, &outb, &errb, false)
		if err != nil {
			if strings.Contains(errb.String(), "No such file or directory") {
				continue
			}
			return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, outb.String(), errb.String())
		}

		log.Info("Pod is waiting for recovery", "pod", pod.Name, "gtidExecuted", outb.String())

		podFQDN := fmt.Sprintf("%s.%s.%s", pod.Name, mysql.ServiceName(cr), cr.Namespace)
		podUri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, podFQDN)

		mysh, err := mysqlsh.NewWithExec(&pod, podUri)
		if err != nil {
			return err
		}

		err = mysh.RebootClusterFromCompleteOutageWithExec(ctx, cr.InnoDBClusterName())
		if err == nil {
			log.Info("Cluster was successfully rebooted")
			r.Recorder.Event(cr, "Normal", "FullClusterCrashRecovered", "Cluster recovered from full cluster crash")
			err := r.cleanupFullClusterCrashFile(ctx, cr)
			if err != nil {
				log.Error(err, "failed to remove /var/lib/mysql/full-cluster-crash")
			}
			break
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) cleanupFullClusterCrashFile(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx)

	cli, err := clientcmd.NewClient()
	if err != nil {
		return err
	}

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr))
	if err != nil {
		return errors.Wrap(err, "get pods")
	}

	var outb, errb bytes.Buffer
	cmd := []string{"rm", "/var/lib/mysql/full-cluster-crash"}
	for _, pod := range pods {
		err = cli.Exec(ctx, &pod, "mysql", cmd, nil, &outb, &errb, false)
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
