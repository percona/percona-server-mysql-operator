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
