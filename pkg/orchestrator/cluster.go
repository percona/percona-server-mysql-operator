package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"

	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
)

var ErrSplitTopology = errors.New("orchestrator sees more than one live cluster")

// ResolveCluster returns the name of the one cluster the orchestrator of this
// cluster holds live instances of. The alias does not do: every primary claims
// it through DetectClusterAliasQuery, so a primary left outside the topology
// takes the alias over and a lookup by it lands on either cluster.
func ResolveCluster(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, clusterHint string) (string, error) {
	var res, errb bytes.Buffer
	if err := exec(ctx, cliCmd, pod, "api/all-instances", &res, &errb); err != nil {
		return "", err
	}

	var instances []*Instance
	if err := json.Unmarshal(res.Bytes(), &instances); err != nil {
		orcResp := new(orcResponse)
		if err := unmarshalOrcResponse(res.Bytes(), orcResp); err != nil {
			return "", err
		}
		if err := orcResp.Error(); err != nil {
			return "", err
		}
	}

	return liveCluster(instances, clusterHint)
}

// liveCluster leaves out the clusters with no reachable instance: a failover
// leaves the dead primary behind as a cluster of its own. With nothing
// reachable there is nothing to tell apart, and the hint is as good as any.
func liveCluster(instances []*Instance, clusterHint string) (string, error) {
	seen := make(map[string]bool)
	var live []string
	for _, instance := range instances {
		if !instance.IsLastCheckValid || instance.ClusterName == "" || seen[instance.ClusterName] {
			continue
		}
		seen[instance.ClusterName] = true
		live = append(live, instance.ClusterName)
	}

	switch len(live) {
	case 0:
		return clusterHint, nil
	case 1:
		return live[0], nil
	}

	sort.Strings(live)
	return "", fmt.Errorf("%w: %s", ErrSplitTopology, strings.Join(live, ", "))
}
