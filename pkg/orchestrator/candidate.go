package orchestrator

import "sort"

// ReplicasOf returns the instances replicating from host.
func ReplicasOf(instances []*Instance, host string) []*Instance {
	replicas := make([]*Instance, 0, len(instances))

	for _, instance := range instances {
		if instance.MasterKey.Hostname == host {
			replicas = append(replicas, instance)
		}
	}

	return replicas
}

// BestCandidate returns the replica best suited for promotion, whichever
// instance it currently replicates from. It is the pick for a promotion forced
// by hand, where the old primary may be gone from the topology already.
func BestCandidate(instances []*Instance) (InstanceKey, bool) {
	replicas := make([]*Instance, 0, len(instances))

	for _, instance := range instances {
		if instance.MasterKey.Hostname != "" {
			replicas = append(replicas, instance)
		}
	}

	if len(replicas) == 0 {
		return InstanceKey{}, false
	}

	return MostUpToDate(replicas), true
}

// MostUpToDate returns the replica with the least to catch up on.
func MostUpToDate(replicas []*Instance) InstanceKey {
	sort.SliceStable(replicas, func(i, j int) bool {
		return betterCandidate(replicas[i], replicas[j])
	})

	return replicas[0].Key
}

func betterCandidate(a, b *Instance) bool {
	if (len(a.Problems) == 0) != (len(b.Problems) == 0) {
		return len(a.Problems) == 0
	}

	if a.ExecBinlogCoordinates != b.ExecBinlogCoordinates {
		return ahead(a.ExecBinlogCoordinates, b.ExecBinlogCoordinates)
	}

	return a.Key.Hostname < b.Key.Hostname
}

func ahead(a, b BinlogCoordinates) bool {
	if a.LogFile != b.LogFile {
		return a.LogFile > b.LogFile
	}

	return a.LogPos > b.LogPos
}
