package topology

import (
	"context"
	"fmt"
	"sort"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
)

func experimentalGetAsync(ctx context.Context, m Manager, hosts ...string) (Topology, error) {
	t := new(Topology)
	for _, host := range hosts {
		if err := recursiveAsyncDiscover(ctx, m, host, replicator.ReplicationStatusNotInitiated, t); err != nil {
			return Topology{}, err
		}
	}

	// The first call to the recursiveAsyncDiscover has `ReplicationStatusNotInitiated`
	// Because of it, if there is only one pod in the topology, it will be assigned as a replica
	// That's why we should assign the first replica in topology as primary and delete it from the replicas slice
	if t.Primary == "" && len(t.Replicas) > 0 {
		sort.Strings(t.Replicas)
		t.SetPrimary(t.Replicas[0]) // this will also delete it from replica slice
	}
	return *t, nil
}

func getAsync(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, cl client.Reader) (Topology, error) {
	log := logf.FromContext(ctx).WithName("GetAsync")

	pod, err := getOrcPod(ctx, cl, cr, 0)
	if err != nil {
		log.Info("orchestrator pod is not found: " + err.Error() + ". skip")
		return Topology{}, nil
	}
	if err := orchestrator.DiscoverExec(ctx, pod, mysql.ServiceName(cr), mysql.DefaultPort); err != nil {
		switch err.Error() {
		case "Unauthorized":
			log.Info("mysql is not ready, unauthorized orchestrator discover response. skip")
			return Topology{}, nil
		case orchestrator.ErrEmptyResponse.Error():
			log.Info("mysql is not ready, empty orchestrator discover response. skip")
			return Topology{}, nil
		}
		return Topology{}, errors.Wrap(err, "failed to discover cluster")
	}
	primary, err := orchestrator.ClusterPrimaryExec(ctx, pod, cr.ClusterHint())
	if err != nil {
		return Topology{}, errors.Wrap(err, "get primary")
	}
	if primary.Alias == "" {
		log.Info("mysql is not ready, orchestrator cluster primary alias is empty. skip")
		return Topology{}, nil
	}
	if primary.Key.Hostname == "" {
		primary.Key.Hostname = fmt.Sprintf("%s.%s.%s", primary.Alias, mysql.ServiceName(cr), cr.Namespace)
	}
	replicas := make([]string, 0, len(primary.Replicas))
	for _, r := range primary.Replicas {
		replicas = append(replicas, r.Hostname)
	}
	return Topology{
		Primary:  primary.Key.Hostname,
		Replicas: replicas,
	}, nil
}

func getOrcPod(ctx context.Context, cl client.Reader, cr *apiv1alpha1.PerconaServerMySQL, idx int) (*corev1.Pod, error) {
	pod := &corev1.Pod{}

	nn := types.NamespacedName{Namespace: cr.Namespace, Name: orchestrator.PodName(cr, idx)}
	if err := cl.Get(ctx, nn, pod); err != nil {
		return nil, err
	}

	return pod, nil
}

func recursiveAsyncDiscover(ctx context.Context, m Manager, host string, replicaStatus replicator.ReplicationStatus, t *Topology) error {
	var readOnly bool
	var replicas []string
	var source string
	var status replicator.ReplicationStatus
	var failedToConnect bool
	err := func() error {
		db, err := m.Replicator(ctx, host)
		if err != nil {
			// We should ignore connection errors because function can try to connect to starting pod
			failedToConnect = true
			return nil
		}
		defer db.Close()
		readOnly, err = db.IsReadonly(ctx)
		if err != nil {
			return errors.Wrap(err, "check readonly")
		}
		host, err = db.ReportHost(ctx)
		if err != nil {
			return errors.Wrap(err, "failed to get report host")
		}
		replicas, err = db.ShowReplicas(ctx)
		if err != nil {
			return errors.Wrap(err, "get replicas")
		}
		status, _, err = db.ReplicationStatus(ctx)
		if err != nil {
			return errors.Wrap(err, "check replication status")
		}
		source, err = getSource(ctx, db)
		if err != nil {
			return errors.Wrap(err, "get primary")
		}
		return nil
	}()
	if err != nil {
		return errors.Wrap(err, "failed to retrieve replication info")
	}
	if failedToConnect {
		return nil
	}
	// If a pod has `read_only` set to `true` we should add it to replicas slice
	if readOnly {
		t.AddReplica(host)
	} else {
		// If a pod has `read_only` set to `false` we should set it as primary
		//
		// Pods that are not bootstrapped also have `read_only` set to `false`
		// That is why we should check if something is already replicating from this pod
		// If not, we should add current pod to replicas, because it will be used as a replica in the future
		//
		// The first call to the recursiveAsyncDiscover has `ReplicationStatusNotInitiated`
		// We are not afraid of it, as we will call this function again with the correct status obtained from calls to replicas below
		if replicaStatus == replicator.ReplicationStatusActive {
			if t.Primary != "" && t.Primary != host {
				return errors.Errorf("multiple primaries detected: %s and %s", t.Primary, host)
			}
			t.SetPrimary(host) // This method will delete `host` from `replicas` and assign it to primary
		} else {
			t.AddReplica(host) // If primary == `host` it will be ignored
		}
	}
	for _, replica := range replicas {
		if t.HasReplica(replica) {
			continue
		}
		err := recursiveAsyncDiscover(ctx, m, replica, status, t)
		if err != nil {
			return errors.Wrapf(err, "failed to discover %s", replica)
		}
	}

	if source != "" {
		err = recursiveAsyncDiscover(ctx, m, source, status, t)
		return errors.Wrapf(err, "failed to discover %s", source)
	}

	return nil
}

func getSource(ctx context.Context, db replicator.Replicator) (string, error) {
	status, err := db.ShowReplicaStatus(ctx)
	if err != nil {
		return "", errors.Wrap(err, "get replica status")
	}
	primaryHost, ok := status["Source_Host"]
	if !ok {
		return "", nil
	}
	return primaryHost, nil
}
