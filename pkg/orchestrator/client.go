package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/pkg/errors"
)

type CKey struct {
	Hostname string `json:"Hostname"`
	Port     int32  `json:"Port"`
}

type clusterImpl struct {
	Key           CKey   `json:"Key"`
	InstanceAlias string `json:"InstanceAlias"`
	MasterKey     CKey   `json:"MasterKey"`
	Replicas      []CKey `json:"Replicas"`
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
	primary := &clusterImpl{}
	return primary, doRequest(ctx, host+"/api/master/"+clusterHint, primary)
}

func doRequest(ctx context.Context, url string, o interface{}) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errors.Wrap(err, "make request")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "do request")
	}
	defer res.Body.Close()

	if err := json.NewDecoder(res.Body).Decode(o); err != nil {
		return errors.Wrap(err, "json decode")
	}

	return nil
}
