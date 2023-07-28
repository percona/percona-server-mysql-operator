package topology

import (
	"context"

	"github.com/pkg/errors"
)

func getGRTopology(ctx context.Context, m Manager, hostname string) (Topology, error) {
	db, err := m.Replicator(ctx, hostname)
	if err != nil {
		return Topology{}, errors.Wrapf(err, "open connection to %s", hostname)
	}
	defer db.Close()

	replicas, err := db.GetGroupReplicationReplicas(ctx)
	if err != nil {
		return Topology{}, errors.Wrap(err, "get group-replication replicas")
	}

	primary, err := db.GetGroupReplicationPrimary(ctx)
	if err != nil {
		return Topology{}, errors.Wrap(err, "get group-replication primary")
	}
	return Topology{
		Primary:  primary,
		Replicas: replicas,
	}, nil
}
