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
type cluster []instance

func (c *cluster) Primary() (*instance, error) {
	for _, inst := range *c {
		if inst.MasterKey.Hostname == "" {
			return &inst, nil
		}
	}

	return nil, errors.New("no primary")
}

func (c *cluster) Replicas() ([]instanceKey, error) {
	for _, inst := range *c {
		if inst.MasterKey.Hostname == "" {
			continue
		}

		return inst.Replicas, nil
	}

	return nil, errors.New("no primary")
}

func New(host string) *orcClient {
	c := &http.Client{
		Timeout: time.Second * 5,
	}

	return &orcClient{
		host:   host,
		client: c,
	}
}

func (o *orcClient) ClusterPrimary(clusterHint string) (*instance, error) {
	req, err := http.NewRequest(http.MethodGet, o.host+"/api/master/"+clusterHint, nil)
	if err != nil {
		return nil, errors.Wrap(err, "build request")
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "make request")
	}
	defer resp.Body.Close()

	primary := &instance{}
	if err := json.NewDecoder(resp.Body).Decode(primary); err != nil {
		return nil, errors.Wrap(err, "json decode")
	}

	return primary, nil
}

func (o *orcClient) Cluster(clusterHint string) (cluster, error) {
	req, err := http.NewRequest(http.MethodGet, o.host+"/api/cluster/"+clusterHint, nil)
	if err != nil {
		return nil, errors.Wrap(err, "build request")
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "make request")
	}
	defer resp.Body.Close()

	c := make(cluster, 0)
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return nil, errors.Wrap(err, "json decode")
	}

	return c, nil
}
