package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/pkg/errors"
)

type CKey struct {
	Hostname string
	Port     int32
}

type clusterImpl struct {
	Key           CKey
	InstanceAlias string
	MasterKey     CKey
	Replicas      []CKey
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

func ClusterPrimary(ctx context.Context, host, clusterHint string) (Cluster, error) {
	var primary *clusterImpl
	return primary, doRequest(ctx, host+"/api/master/"+clusterHint, primary)
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
