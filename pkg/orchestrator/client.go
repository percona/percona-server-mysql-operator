package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/pkg/errors"
)

type orcResponse struct {
	Code    string      `json:"Code"`
	Message string      `json:"Message"`
	Details interface{} `json:"Details,omitempty"`
}

type InstanceKey struct {
	Hostname string `json:"Hostname"`
	Port     int32  `json:"Port"`
}

type Instance struct {
	Key       InstanceKey   `json:"Key"`
	Alias     string        `json:"InstanceAlias"`
	MasterKey InstanceKey   `json:"MasterKey"`
	Replicas  []InstanceKey `json:"Replicas"`
}

func ClusterPrimary(ctx context.Context, apiHost, clusterHint string) (*Instance, error) {
	primary := &Instance{}
	return primary, doRequest(ctx, apiHost+"/api/master/"+clusterHint, primary)
}

func StopReplication(ctx context.Context, apiHost, host string, port int32) error {
	resp := &orcResponse{}

	url := fmt.Sprintf("%s/api/stop-replica/%s/%d", apiHost, host, port)
	if err := doRequest(ctx, url, resp); err != nil {
		return errors.Wrapf(err, "do request to %s", url)
	}

	if resp.Code != "OK" {
		return errors.New(resp.Message)
	}

	return nil
}

func StartReplication(ctx context.Context, apiHost, host string, port int32) error {
	resp := &orcResponse{}

	url := fmt.Sprintf("%s/api/start-replica/%s/%d", apiHost, host, port)
	if err := doRequest(ctx, url, resp); err != nil {
		return errors.Wrapf(err, "do request to %s", url)
	}

	if resp.Code != "OK" {
		return errors.New(resp.Message)
	}

	return nil
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

	if res.StatusCode >= 400 && res.StatusCode <= 599 {
		return errors.Errorf("request failed with %s", res.Status)
	}

	if err := json.NewDecoder(res.Body).Decode(o); err != nil {
		return errors.Wrap(err, "json decode")
	}

	return nil
}
