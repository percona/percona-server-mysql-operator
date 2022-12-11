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

var ErrPodNotYetRunning = errors.New("pod not yet running")

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

	// Because of removing `report_host=${FQDN}` from entrypoint, MEMBER_HOST is now only the hostname,
	// like `cluster1-mysql-0` instead of FQDN lke `cluster1-mysql-0.cluster1-mysql.default`
	primaryHostname, err := r.getPrimaryFromGR(ctx, cr)
	if err != nil {
		// TODO: handle initial `connection refused` errors.
		// 	while the pod gets ready right after cluster bootstrap, we will get bunch of these errors
		return errors.Wrap(err, "get GR primary")
	}
	l.Info(fmt.Sprintf("Primary hostname: %s", primaryHostname))

	primaryFQDN := fmt.Sprintf("%s.%s.%s", primaryHostname, mysql.ServiceName(cr), cr.Namespace)
	l.Info(fmt.Sprintf("Primary FQDN: %s", primaryFQDN))

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	// primaryFQDN := fmt.Sprintf("%s.%s.%s", primary.Name, mysql.ServiceName(cr), cr.Namespace)
	primaryUri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, primaryFQDN)

	mysh := mysqlsh.New(k8sexec.New(), primaryUri)

	// TODO: Check does the cluster exist?
	// 		Can we get a full cluster outage case here as well?

	endpoints := &corev1.Endpoints{}
	err = r.Client.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: mysql.ServiceName(cr)}, endpoints)
	if err != nil {
		return errors.Wrap(err, "get endpoints")
	}
	l.Info(fmt.Sprintf("Endpoints unready addresses: %v", endpoints.Subsets[0].NotReadyAddresses))

	for _, addr := range endpoints.Subsets[0].NotReadyAddresses {
		l.Info(fmt.Sprintf("Handling not ready instance - pod IP: %s, pod hostname: %s", addr.IP, addr.Hostname))

		instance := fmt.Sprintf("%s:%d", addr.IP, mysql.DefaultPort)
		state, err := mysh.MemberState(ctx, cr.InnoDBClusterName(), instance)
		if err != nil && !errors.Is(err, innodbcluster.ErrMemberNotFound) {
			return errors.Wrapf(err, "get member state of %s", addr.Hostname)
		}

		if errors.Is(err, innodbcluster.ErrMemberNotFound) {
			l.Info(fmt.Sprintf("Configuring instace: %s, %s", addr.Hostname, addr.IP))
			podUri := fmt.Sprintf("%s:%s@%s", apiv1alpha1.UserOperator, operatorPass, addr.IP)
			if err := mysh.ConfigureInstance(ctx, podUri); err != nil {
				return errors.Wrapf(err, "configure instance %s", addr.Hostname)
			}
			l.Info("Configured instance", "pod", addr.Hostname)

			l.Info(fmt.Sprintf("Adding instace: %s, %s", addr.Hostname, addr.IP))
			if err := mysh.AddInstance(ctx, cr.InnoDBClusterName(), podUri); err != nil {
				return errors.Wrapf(err, "add instance %s", addr.Hostname)
			}
			l.Info("Added instance to the cluster", "cluster", cr.Name, "pod", addr.Hostname)
		}

		l.V(1).Info("Member state", "pod", addr.Hostname, "state", state)
		if state == innodbcluster.MemberStateMissing {
			l.Info(fmt.Sprintf("Rejoining instace: %s, %s", addr.Hostname, addr.IP))
			if err := mysh.RejoinInstance(ctx, cr.InnoDBClusterName(), addr.IP); err != nil {
				return errors.Wrapf(err, "rejoin instance %s", addr.Hostname)
			}
			l.Info("Instance rejoined", "pod", addr.Hostname)
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

	peers, err := lookup(ctx, mysql.UnreadyServiceName(cr))
	if err != nil && errors.Is(err, ErrPodNotYetRunning) {
		l.Info("Waiting for seed to start running")
		return false, nil
	}
	if err != nil {
		return false, err
	}
	l.Info(fmt.Sprintf("Peers: %v", peers.List()))

	if k8s.IsPodReady(*seed) {
		l.Info(fmt.Sprintf("Seed pod %s already configured and part of the cluster", seed.Name))
		return true, nil
	}

	//seedFQDN := fmt.Sprintf("%s.%s.%s", seed.Name, mysql.UnreadyServiceName(cr), cr.Namespace)
	seedFQDN := peers.List()[0]
	l.Info(fmt.Sprintf("Seed FQDN: %s", seedFQDN))

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

	return true, nil
}

func lookup(ctx context.Context, svcName string) (sets.String, error) {
	l := log.FromContext(ctx).WithName("reconcileGroupReplication")

	endpoints := sets.NewString()
	_, srvRecords, err := net.LookupSRV("", "", svcName)
	if err != nil {
		if strings.Contains(err.Error(), "no such host") {
			return endpoints, ErrPodNotYetRunning
		}
		return endpoints, err
	}
	for _, srvRecord := range srvRecords {
		// The SRV records have the pattern $HOSTNAME.$SERVICE.$.NAMESPACE.svc.$CLUSTER_DNS_SUFFIX
		// We only want $HOSTNAME.$SERVICE.$NAMESPACE because in the `selectDonor` function we
		// compare the list generated here with the output of the `getFQDN` function
		srv := strings.Split(srvRecord.Target, ".")
		ep := strings.Join(srv[:3], ".")
		l.Info("Lookup target: %s, endpoint: %s", srvRecord.Target, ep)

		endpoints.Insert(ep)
	}
	return endpoints, nil
}
