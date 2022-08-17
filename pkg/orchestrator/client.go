package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/pkg/errors"
)

type OrcResponse struct {
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
	url := fmt.Sprintf("%s/api/master/%s", apiHost, clusterHint)

	resp, err := doRequest(ctx, url)
	if err != nil {
		return nil, errors.Wrapf(err, "do request to %s", url)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "read response body")
	}

	primary := &Instance{}
	if err := json.Unmarshal(body, primary); err == nil {
		return primary, nil
	}

	orcResp := &OrcResponse{}
	if err := json.Unmarshal(body, orcResp); err != nil {
		return nil, errors.Wrap(err, "json decode")
	}

	if orcResp.Code == "ERROR" {
		return nil, errors.New(orcResp.Message)
	}

	return primary, nil
}

func StopReplication(ctx context.Context, apiHost, host string, port int32) error {
	url := fmt.Sprintf("%s/api/stop-replica/%s/%d", apiHost, host, port)

	resp, err := doRequest(ctx, url)
	if err != nil {
		return errors.Wrapf(err, "do request to %s", url)
	}
	defer resp.Body.Close()

	orcResp := &OrcResponse{}
	if err := json.NewDecoder(resp.Body).Decode(orcResp); err != nil {
		return errors.Wrap(err, "json decode")
	}

	if orcResp.Code == "ERROR" {
		return errors.New(orcResp.Message)
	}

	return nil
}

func StartReplication(ctx context.Context, apiHost, host string, port int32) error {
	url := fmt.Sprintf("%s/api/start-replica/%s/%d", apiHost, host, port)

	resp, err := doRequest(ctx, url)
	if err != nil {
		return errors.Wrapf(err, "do request to %s", url)
	}
	defer resp.Body.Close()

	orcResp := &OrcResponse{}
	if err := json.NewDecoder(resp.Body).Decode(orcResp); err != nil {
		return errors.Wrap(err, "json decode")
	}

	if orcResp.Code == "ERROR" {
		return errors.New(orcResp.Message)
	}

	return nil
}

func AddPeer(ctx context.Context, apiHost string, peer string) error {
	url := fmt.Sprintf("%s/api/raft-add-peer/%s", apiHost, peer)

	resp, err := doRequest(ctx, url)
	if err != nil {
		return errors.Wrapf(err, "do request to %s", url)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrap(err, "read response body")
	}

	// Orchestrator returns peer IP as string on success
	o := ""
	if err := json.Unmarshal(body, &o); err == nil {
		return nil
	}

	orcResp := &OrcResponse{}
	if err := json.Unmarshal(body, &orcResp); err != nil {
		return errors.Wrap(err, "json decode")
	}

	if orcResp.Code == "ERROR" {
		return errors.New(orcResp.Message)
	}

	return nil
}

func RemovePeer(ctx context.Context, apiHost string, peer string) error {
	url := fmt.Sprintf("%s/api/raft-remove-peer/%s", apiHost, peer)

	resp, err := doRequest(ctx, url)
	if err != nil {
		return errors.Wrapf(err, "do request to %s", url)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrap(err, "read response body")
	}

	// Orchestrator returns peer IP as string on success
	o := ""
	if err := json.Unmarshal(body, &o); err == nil {
		return nil
	}

	orcResp := &OrcResponse{}
	if err := json.Unmarshal(body, &orcResp); err != nil {
		return errors.Wrap(err, "json decode")
	}

	if orcResp.Code == "ERROR" {
		return errors.New(orcResp.Message)
	}

	return nil
}

func doRequest(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.Wrap(err, "make request")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "do request")
	}

	return resp, nil
}

func GetReplicationStatus(ctx context.Context, apiHost string, clusterName string) (orcResp *OrcResponse, err error) {
	url := fmt.Sprintf("%s/api/replication-analysis/%s", apiHost, clusterName)

	resp, err := doRequest(ctx, url)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if err = json.NewDecoder(resp.Body).Decode(orcResp); err != nil {
		return
	}

	return
}
