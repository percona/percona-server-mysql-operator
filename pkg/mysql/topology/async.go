package topology

import (
	"context"
	"sort"

	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
)

func GetAsync(ctx context.Context, operatorPass string, hosts ...string) (Topology, error) {
	t := new(Topology)
	for _, host := range hosts {
		if err := recursiveAsyncDiscover(ctx, host, operatorPass, replicator.ReplicationStatusNotInitiated, t); err != nil {
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

func recursiveAsyncDiscover(ctx context.Context, host, operatorPass string, replicaStatus replicator.ReplicationStatus, t *Topology) error {
	var readOnly bool
	var replicas []string
	var source string
	var status replicator.ReplicationStatus
	var failedToConnect bool
	err := func() error {
		db, err := replicator.NewReplicator(apiv1alpha1.UserOperator,
			operatorPass,
			host,
			mysql.DefaultAdminPort)
		if err != nil {
			// We should ignore connection errors because function can try to connect to starting pod
			failedToConnect = true
			return nil
		}
		defer db.Close()
		readOnly, err = db.IsReadonly()
		if err != nil {
			return errors.Wrap(err, "check readonly")
		}
		host, err = db.ReportHost()
		if err != nil {
			return errors.Wrap(err, "failed to get report host")
		}
		replicas, err = db.ShowReplicas(ctx)
		if err != nil {
			return errors.Wrap(err, "get replicas")
		}
		status, _, err = db.ReplicationStatus()
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
		err := recursiveAsyncDiscover(ctx, replica, operatorPass, status, t)
		if err != nil {
			return errors.Wrapf(err, "failed to discover %s", replica)
		}
	}

	if source != "" {
		err = recursiveAsyncDiscover(ctx, source, operatorPass, status, t)
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
