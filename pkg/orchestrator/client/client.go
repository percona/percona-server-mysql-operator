package client

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/pkg/errors"
)

type orcClient struct {
	host   string
	client *http.Client
}

type instanceKey struct {
	Hostname string
	Port     int32
}

type clusterImpl struct {
	Key           instanceKey
	InstanceAlias string

	MasterKey instanceKey
	Replicas  []instanceKey
}

type Cluster interface {
	Hostname() string
	Alias() string
}

func (i clusterImpl) Hostname() string {
	return i.Key.Hostname
}

func (i clusterImpl) Alias() string {
	return i.InstanceAlias
}

func Primary(clusters []Cluster) Cluster {
	for _, c := range clusters {
		if impl, ok := c.(*clusterImpl); ok && impl.MasterKey.Hostname == "" {
			return impl
		}
	}

	return nil
}

func Replicas(clusters []Cluster) []instanceKey {
	c, ok := Primary(clusters).(*clusterImpl)
	if !ok {
		return nil
	}

	return c.Replicas
}

func ClusterPrimary(ctx context.Context, host, clusterHint string) (Cluster, error) {
	var primary *clusterImpl
	return primary, doRequest(ctx, host+"/api/master/"+clusterHint, primary)
}

func ClusterReplicas(ctx context.Context, host, clusterHint string) ([]Cluster, error) {
	var c []Cluster
	return c, doRequest(ctx, host+"/api/cluster/"+clusterHint, c)
}

func doRequest(ctx context.Context, url string, o interface{}) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	res, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errors.Wrap(err, "request")
	}
	res.Body.Close()

	if err := json.NewDecoder(res.Body).Decode(o); err != nil {
		return errors.Wrap(err, "json decode")
	}

	return nil
}
