package controllers

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sexec "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
)

func (r *PerconaServerMySQLReconciler) reconcileGroupReplicationUpgraded(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	l := log.FromContext(ctx).WithName("reconcileGroupReplication")

	if cr.Status.MySQL.Ready != cr.MySQLSpec().Size {
		l.V(1).Info("Waiting for all pods to be ready")
		return nil
	}

	cond := meta.FindStatusCondition(cr.Status.Conditions, apiv1alpha1.InnoDBClusterInitialized)
	if cond == nil || cond.Status == metav1.ConditionFalse {

		err := r.bootstrapInnoDBCluster(ctx, cr)
		if err != nil {
			return err
		}

		meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
			Type:               apiv1alpha1.InnoDBClusterInitialized,
			Status:             metav1.ConditionTrue,
			Reason:             "InnoDBClusterInitialized",
			Message:            fmt.Sprintf("InnoDB cluster successfully initialized with %d nodes", cr.MySQLSpec().Size),
			LastTransitionTime: metav1.Now(),
		})

		l.Info(fmt.Sprintf("%s cluster successfully initialized with %d nodes", cr.InnoDBClusterName(), cr.MySQLSpec().Size))
		return nil
	}

	l.Info("Now we need to keep the cluster running")

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr))
	if err != nil {
		return errors.Wrap(err, "get pods")
	}

	somePod := pods[1]

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	somePodFQDN := fmt.Sprintf("%s.%s.%s", somePod.Name, mysql.ServiceName(cr), cr.Namespace)
	somePodUri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, somePodFQDN)

	mysh := mysqlsh.New(k8sexec.New(), somePodUri)
	exists, err := mysh.DoesClusterExist(ctx, cr.InnoDBClusterName())
	if err != nil {
		l.Error(err, "AAAAA cluster exists failed")
		return err
	}

	if exists {
		l.Info("AAAA - yes, cluster exists")
	}

	top, err := mysh.Topology(ctx, cr.InnoDBClusterName())
	if err != nil {
		l.Error(err, "AAAAA cluster topology failed")
		return err
	}

	l.Info(fmt.Sprintf("AAAAA topology: %v", top))


	// for _, pod := range pods {

	// }

	return nil
}

func (r *PerconaServerMySQLReconciler) bootstrapInnoDBCluster(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	l := log.FromContext(ctx).WithName("reconcileGroupReplication")

	l.Info(fmt.Sprintf("Initialising InnoDB cluster: %s", cr.InnoDBClusterName()))

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr))
	if err != nil {
		return errors.Wrap(err, "get pods")
	}

	// Seed can be whatever node, not necessarily pod-0
	seed := pods[0]

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}
	db, err := replicator.NewReplicator(apiv1alpha1.UserOperator, operatorPass, mysql.FQDN(cr, 0), mysql.DefaultAdminPort)
	if err != nil {
		return errors.Wrapf(err, "connect to %s", seed.Name)
	}
	defer db.Close()

	asyncReplicationStatus, _, err := db.ReplicationStatus()
	if err != nil {
		return errors.Wrapf(err, "get async replication status of %s", seed.Name)
	}
	if asyncReplicationStatus == replicator.ReplicationStatusActive {
		l.Info("Replication threads are running. Stopping them before starting group replication", "pod", seed.Name)
		if err := db.StopReplication(); err != nil {
			return err
		}
	}

	seedFQDN := fmt.Sprintf("%s.%s.%s", seed.Name, mysql.ServiceName(cr), cr.Namespace)
	seedUri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, seedFQDN)

	mysh := mysqlsh.New(k8sexec.New(), seedUri)

	clusterExists, err := mysh.DoesClusterExist(ctx, cr.InnoDBClusterName())
	if err != nil {
		if errors.Is(err, mysqlsh.ErrMetadataExistsButGRNotActive) {
			l.Info("Rebooting cluster from complete outage")
			if err := mysh.RebootClusterFromCompleteOutage(ctx, cr.InnoDBClusterName(), []string{seedFQDN}); err != nil {
				return err
			}
			l.Info("Successfully rebooted cluster")
			return nil
		}
		return errors.Wrapf(err, "check if InnoDB Cluster %s exists", cr.InnoDBClusterName())
	}

	if clusterExists {
		l.Info(fmt.Sprintf("Aborting InnoDB cluster bootstrap, cluster %s already exists", cr.InnoDBClusterName()))
		return nil
	}

	l.Info("Configuring seed instace", "instace", seedFQDN)
	if err := mysh.ConfigureInstance(ctx, seedUri); err != nil {
		return err
	}
	l.Info("Configured seed instance", "instance", seedFQDN)

	l.Info("Creating InnoDB cluster")
	err = mysh.CreateCluster(ctx, cr.InnoDBClusterName())
	if err != nil {
		return errors.Wrapf(err, "create cluster %s", cr.InnoDBClusterName())
	}
	l.Info("Created InnoDB Cluster", "cluster", cr.InnoDBClusterName())

	for _, pod := range pods {
		if pod.Name == seed.Name {
			continue
		}

		podFQDN := fmt.Sprintf("%s.%s.%s", pod.Name, mysql.ServiceName(cr), cr.Namespace)
		db, err := replicator.NewReplicator(apiv1alpha1.UserOperator, operatorPass, podFQDN, mysql.DefaultAdminPort)
		if err != nil {
			return errors.Wrapf(err, "connect to %s", pod.Name)
		}
		defer db.Close()

		asyncReplicationStatus, _, err := db.ReplicationStatus()
		if err != nil {
			return errors.Wrapf(err, "get async replication status of %s", pod.Name)
		}
		if asyncReplicationStatus == replicator.ReplicationStatusActive {
			l.Info("Replication threads are running. Stopping them before starting group replication", "pod", pod.Name)
			if err := db.StopReplication(); err != nil {
				return err
			}
		}

		podUri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, podFQDN)
		if err := mysh.ConfigureInstance(ctx, podUri); err != nil {
			return errors.Wrapf(err, "configure instance %s", pod.Name)
		}
		l.Info("Configured instance", "pod", pod.Name)

		if err := mysh.AddInstance(ctx, cr.InnoDBClusterName(), podUri); err != nil {
			return errors.Wrapf(err, "add instance %s", pod.Name)
		}
		l.Info("Added instance to the cluster", "cluster", cr.Name, "pod", pod.Name)
	}

	return nil
}
