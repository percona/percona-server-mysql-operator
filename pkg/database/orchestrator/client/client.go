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

type clusterMasterResp struct {
	InstanceAlias string
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

func (c *cluster) Master() (*instance, error) {
	for _, inst := range *c {
		if inst.MasterKey.Hostname == "" {
			return &inst, nil
		}
	}

	return nil, errors.New("no master")
}

func (c *cluster) Replicas() ([]instanceKey, error) {
	for _, inst := range *c {
		if inst.MasterKey.Hostname == "" {
			continue
		}

		return inst.Replicas, nil
	}

	return nil, errors.New("no master")
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

func (o *orcClient) ClusterMaster(clusterHint string) (*clusterMasterResp, error) {
	req, err := http.NewRequest(http.MethodGet, o.host+"/api/master/"+clusterHint, nil)
	if err != nil {
		return nil, errors.Wrap(err, "build request")
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "make request")
	}
	defer resp.Body.Close()

	cmr := &clusterMasterResp{}
	if err := json.NewDecoder(resp.Body).Decode(cmr); err != nil {
		return nil, errors.Wrap(err, "json decode")
	}

	return cmr, nil
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
