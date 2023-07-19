package topology

import "github.com/pkg/errors"

func getGRTopology(m Manager, hostname string) (Topology, error) {
	db, err := m.Replicator(hostname)
	if err != nil {
		return Topology{}, errors.Wrapf(err, "open connection to %s", hostname)
	}
	defer db.Close()

	replicas, err := db.GetGroupReplicationReplicas()
	if err != nil {
		return Topology{}, errors.Wrap(err, "get group-replication replicas")
	}

	primary, err := db.GetGroupReplicationPrimary()
	if err != nil {
		return Topology{}, errors.Wrap(err, "get group-replication primary")
	}
	return Topology{
		Primary:  primary,
		Replicas: replicas,
	}, nil
}
