package controllers

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	k8sexec "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
)

func (r *PerconaServerMySQLReconciler) reconcileGroupReplicationUpgraded(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	l := log.FromContext(ctx).WithName("reconcileGroupReplication")

	cond := meta.FindStatusCondition(cr.Status.Conditions, apiv1alpha1.InnoDBClusterInitialized)
	if cond == nil || cond.Status == metav1.ConditionFalse {
		initialized, err := r.bootstrapInnoDBCluster(ctx, cr)
		if err != nil {
			return err
		}

		if !initialized {
			l.Info(fmt.Sprintf("Custer %q is being initialized", cr.InnoDBClusterName()))
			return nil
		}

		meta.SetStatusCondition(&cr.Status.Conditions, metav1.Condition{
			Type:               apiv1alpha1.InnoDBClusterInitialized,
			Status:             metav1.ConditionTrue,
			Reason:             "InnoDBClusterInitialized",
			Message:            fmt.Sprintf("InnoDB cluster initialized with %d nodes", cr.MySQLSpec().Size),
			LastTransitionTime: metav1.Now(),
		})

		l.Info(fmt.Sprintf("InnoDB cluster %q initialized", cr.InnoDBClusterName()))
		return nil
	}

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr))
	if err != nil {
		return errors.Wrap(err, "get pods")
	}

	// When we get the primary, that means it is operational within a good cluster
	primary, err := r.getGRPrimary(ctx, cr, pods)
	if err != nil {
		return errors.Wrap(err, "get GR primary")
	}
	l.Info(fmt.Sprintf("Some pod is: %s", primary.Name))

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	somePodFQDN := fmt.Sprintf("%s.%s.%s", primary.Name, mysql.ServiceName(cr), cr.Namespace)
	somePodUri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, somePodFQDN)

	mysh := mysqlsh.New(k8sexec.New(), somePodUri)

	// TODO: Check does the cluster exist?
	// Can we get a full cluster outage case here as well?

	for _, pod := range pods {
		if k8s.IsPodReady(pod) {
			l.Info(fmt.Sprintf("Pod %s ready and part of the InnoDB cluster %s", pod.Name, cr.InnoDBClusterName()))
			continue
		}

		podFQDN := fmt.Sprintf("%s.%s.%s", pod.Name, mysql.ServiceName(cr), cr.Namespace)

		instance := fmt.Sprintf("%s:%d", podFQDN, mysql.DefaultPort)
		state, err := mysh.MemberState(ctx, cr.InnoDBClusterName(), instance)
		if err != nil && !errors.Is(err, innodbcluster.ErrMemberNotFound) {
			return errors.Wrapf(err, "get member state of %s", pod.Name)
		}

		if errors.Is(err, innodbcluster.ErrMemberNotFound) {
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

		l.V(1).Info("Member state", "pod", pod.Name, "state", state)
		if state == innodbcluster.MemberStateMissing {
			if err := mysh.RejoinInstance(ctx, cr.InnoDBClusterName(), podFQDN); err != nil {
				return errors.Wrapf(err, "rejoin instance %s", pod.Name)
			}
			l.Info("Instance rejoined", "pod", pod.Name)
			continue
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) bootstrapInnoDBCluster(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) (bool, error) {
	l := log.FromContext(ctx).WithName("reconcileGroupReplication")
	l.Info(fmt.Sprintf("Initialising InnoDB cluster: %s", cr.InnoDBClusterName()))

	seed := &corev1.Pod{}

	err := r.Client.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: mysql.PodName(cr, 0)}, seed)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			l.Info("Initial pod not found")
			return false, nil
		}

		return false, errors.Wrap(err, "get first MySQL pod")
	}

	peers, err := lookup(mysql.UnreadyServiceName(cr))
	if err != nil {
		// return false, errors.Wrap(err, "lookup peers")
		l.Info(fmt.Sprintf("AAAA ERRRRROR peers: %v, error: %s", peers.List(), err.Error()))

		peers, err = lookup(mysql.UnreadyServiceName(cr) + "." + cr.Namespace + ".svc.cluster.local")
		if err != nil {
			return false, errors.Wrap(err, "lookup peers")
		}
	}
	l.Info(fmt.Sprintf("AAAA peers: %v", peers.List()))

	if k8s.IsPodReady(*seed) {
		l.Info(fmt.Sprintf("Seed pod %s already configured and part of the cluster", seed.Name))
		return true, nil
	}

	//cluster1-mysql-0.cluster1-mysql.default.svc.cluster.local

	//seedFQDN := fmt.Sprintf("%s.%s.%s", seed.Name, mysql.UnreadyServiceName(cr), cr.Namespace)

	seedFQDN := peers.List()[0]

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return false, errors.Wrap(err, "get operator password")
	}
	db, err := replicator.NewReplicator(apiv1alpha1.UserOperator, operatorPass, seedFQDN, mysql.DefaultAdminPort)
	if err != nil {
		return false, errors.Wrapf(err, "connect to %s", seed.Name)
	}
	defer db.Close()

	asyncReplicationStatus, _, err := db.ReplicationStatus()
	if err != nil {
		return false, errors.Wrapf(err, "get async replication status of %s", seed.Name)
	}
	if asyncReplicationStatus == replicator.ReplicationStatusActive {
		l.Info("Replication threads are running. Stopping them before starting group replication", "pod", seed.Name)
		if err := db.StopReplication(); err != nil {
			return false, err
		}
	}

	seedUri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, seedFQDN)

	mysh := mysqlsh.New(k8sexec.New(), seedUri)

	clusterExists, err := mysh.DoesClusterExist(ctx, cr.InnoDBClusterName())
	if err != nil {
		if errors.Is(err, mysqlsh.ErrMetadataExistsButGRNotActive) {
			l.Info("Rebooting cluster from complete outage")
			if err := mysh.RebootClusterFromCompleteOutage(ctx, cr.InnoDBClusterName(), []string{seedFQDN}); err != nil {
				return false, err
			}
			l.Info("Successfully rebooted cluster")
			return true, nil
		}
		return false, errors.Wrapf(err, "check if InnoDB Cluster %s exists", cr.InnoDBClusterName())
	}

	if clusterExists {
		l.Info(fmt.Sprintf("Aborting InnoDB cluster bootstrap, cluster %q already exists", cr.InnoDBClusterName()))
		return true, nil
	}

	l.Info(fmt.Sprintf("Configuring seed instace: %s", seedFQDN))
	if err := mysh.ConfigureInstance(ctx, seedUri); err != nil {
		return false, err
	}
	l.Info(fmt.Sprintf("Configured seed instace: %s", seedFQDN))

	l.Info("Creating InnoDB cluster")
	err = mysh.CreateCluster(ctx, cr.InnoDBClusterName())
	if err != nil {
		return false, errors.Wrapf(err, "create cluster %s", cr.InnoDBClusterName())
	}
	l.Info(fmt.Sprintf("Created InnoDB cluster: %s", cr.InnoDBClusterName()))

	// pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr))
	// if err != nil {
	// 	return false, errors.Wrap(err, "get pods")
	// }

	// for _, pod := range pods {
	// 	if pod.Name == seed.Name {
	// 		continue
	// 	}

	// 	if k8s.IsPodReady(pod) {
	// 		l.Info(fmt.Sprintf("Pod %s already configured and part of the cluster", pod.Name))
	// 		continue
	// 	}

	// 	podFQDN := fmt.Sprintf("%s.%s.%s", pod.Name, mysql.UnreadyServiceName(cr), cr.Namespace)

	// 	db, err := replicator.NewReplicator(apiv1alpha1.UserOperator, operatorPass, podFQDN, mysql.DefaultAdminPort)
	// 	if err != nil {
	// 		return false, errors.Wrapf(err, "connect to %s", pod.Name)
	// 	}
	// 	defer db.Close()

	// 	asyncReplicationStatus, _, err := db.ReplicationStatus()
	// 	if err != nil {
	// 		return false, errors.Wrapf(err, "get async replication status of %s", pod.Name)
	// 	}
	// 	if asyncReplicationStatus == replicator.ReplicationStatusActive {
	// 		l.Info("Replication threads are running. Stopping them before starting group replication", "pod", pod.Name)
	// 		if err := db.StopReplication(); err != nil {
	// 			return false, err
	// 		}
	// 	}

	// 	instance := fmt.Sprintf("%s:%d", podFQDN, mysql.DefaultPort)
	// 	state, err := mysh.MemberState(ctx, cr.InnoDBClusterName(), instance)
	// 	if err != nil && !errors.Is(err, innodbcluster.ErrMemberNotFound) {
	// 		return false, errors.Wrapf(err, "get member state of %s", pod.Name)
	// 	}
	// 	l.Info(fmt.Sprintf("Pod %s has state %s", pod.Name, state))

	// 	if errors.Is(err, innodbcluster.ErrMemberNotFound) {
	// 		podUri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, podFQDN)
	// 		if err := mysh.ConfigureInstance(ctx, podUri); err != nil {
	// 			return false, errors.Wrapf(err, "configure instance %s", pod.Name)
	// 		}
	// 		l.Info(fmt.Sprintf("Configured secondary instace: %s", pod.Name))

	// 		if err := mysh.AddInstance(ctx, cr.InnoDBClusterName(), podUri); err != nil {
	// 			return false, errors.Wrapf(err, "add instance %s", pod.Name)
	// 		}
	// 		l.Info(fmt.Sprintf("Added instance %s to the cluster %s", pod.Name, cr.InnoDBClusterName()))
	// 	}
	// }

	return true, nil
}

func lookup(svcName string) (sets.String, error) {
	endpoints := sets.NewString()
	_, srvRecords, err := net.LookupSRV("", "", svcName)
	if err != nil {
		return endpoints, err
	}
	for _, srvRecord := range srvRecords {
		// The SRV records have the pattern $HOSTNAME.$SERVICE.$.NAMESPACE.svc.$CLUSTER_DNS_SUFFIX
		// We only want $HOSTNAME.$SERVICE.$NAMESPACE because in the `selectDonor` function we
		// compare the list generated here with the output of the `getFQDN` function
		srv := strings.Split(srvRecord.Target, ".")
		ep := strings.Join(srv[:3], ".")
		endpoints.Insert(ep)
	}
	return endpoints, nil
}

func (r *PerconaServerMySQLReconciler) getGRPrimary(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, pods []corev1.Pod) (*corev1.Pod, error) {

	return nil, nil
}
