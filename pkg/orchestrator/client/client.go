package client

import (
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

type instance struct {
	Key           instanceKey
	InstanceAlias string

	MasterKey instanceKey
	Replicas  []instanceKey
}

// TODO: Multiple clusters?
type clusters []instance

func (c *clusters) Primary() (*instance, error) {
	for _, inst := range *c {
		if inst.MasterKey.Hostname == "" {
			return &inst, nil
		}
	}

	return nil, errors.New("no primary")
}

func (c *clusters) Replicas() ([]instanceKey, error) {
	for _, inst := range *c {
		if inst.MasterKey.Hostname == "" {
			continue
		}

		return inst.Replicas, nil
	}

	return nil, errors.New("no primary")
}

func New(host string) *orcClient {
	return &orcClient{
		host:   host,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

func (o *orcClient) ClusterPrimary(clusterHint string) (*instance, error) {
	resp, err := o.client.Get(o.host + "/api/master/" + clusterHint)
	if err != nil {
		return nil, errors.Wrap(err, "make request")
	}
	defer resp.Body.Close()

	var primary instance
	if err := json.NewDecoder(resp.Body).Decode(&primary); err != nil {
		return nil, errors.Wrap(err, "json decode")
	}

	return &primary, nil
}

func (o *orcClient) Cluster(clusterHint string) (clusters, error) {
	res, err := o.client.Get(o.host + "/api/cluster/" + clusterHint)
	if err != nil {
		return nil, errors.Wrap(err, "request")
	}
	defer res.Body.Close()

	var c clusters
	if err := json.NewDecoder(res.Body).Decode(&c); err != nil {
		return nil, errors.Wrap(err, "decode")
	}

	return c, nil
}
