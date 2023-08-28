package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

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
	ReadOnly  bool          `json:"ReadOnly"`
}

func ClusterPrimary(ctx context.Context, apiHost, clusterHint string) (*Instance, error) {
	url := fmt.Sprintf("%s/api/master/%s", apiHost, clusterHint)

	resp, err := doRequest(ctx, url)
	if err != nil {
		return nil, errors.Wrapf(err, "do request to %s", url)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "read response body")
	}

	primary := &Instance{}
	if err := json.Unmarshal(body, primary); err == nil {
		return primary, nil
	}

	orcResp := &orcResponse{}
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

	orcResp := &orcResponse{}
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

	orcResp := &orcResponse{}
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrap(err, "read response body")
	}

	// Orchestrator returns peer IP as string on success
	o := ""
	if err := json.Unmarshal(body, &o); err == nil {
		return nil
	}

	orcResp := &orcResponse{}
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrap(err, "read response body")
	}

	// Orchestrator returns peer IP as string on success
	o := ""
	if err := json.Unmarshal(body, &o); err == nil {
		return nil
	}

	orcResp := &orcResponse{}
	if err := json.Unmarshal(body, &orcResp); err != nil {
		return errors.Wrap(err, "json decode")
	}

	if orcResp.Code == "ERROR" {
		return errors.New(orcResp.Message)
	}

	return nil
}

func EnsureNodeIsPrimary(ctx context.Context, apiHost, clusterHint, host string, port int) error {
	primary, err := ClusterPrimary(ctx, apiHost, clusterHint)
	if err != nil {
		return errors.Wrap(err, "get cluster primary")
	}

	if primary.Alias == host {
		return nil
	}

	// /api/graceful-master-takeover-auto/cluster1.default/cluster1-mysql-0/3306
	url := fmt.Sprintf("%s/api/graceful-master-takeover-auto/%s/%s/%d", apiHost, clusterHint, host, port)

	resp, err := doRequest(ctx, url)
	if err != nil {
		return errors.Wrapf(err, "do request to %s", url)
	}
	defer resp.Body.Close()

	orcResp := &orcResponse{}
	if err := json.NewDecoder(resp.Body).Decode(orcResp); err != nil {
		return errors.Wrap(err, "json decode")
	}

	if orcResp.Code == "ERROR" {
		return errors.New(orcResp.Message)
	}

	return nil
}

var ErrEmptyResponse = errors.New("empty response")

func Discover(ctx context.Context, apiHost, host string, port int) error {
	url := fmt.Sprintf("%s/api/discover/%s/%d", apiHost, host, port)

	resp, err := doRequest(ctx, url)
	if err != nil {
		return errors.Wrapf(err, "do request to %s", url)
	}
	defer resp.Body.Close()

	orcResp := new(orcResponse)
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrap(err, "read response body")
	}

	if len(data) == 0 {
		return ErrEmptyResponse
	}

	if err := json.Unmarshal(data, orcResp); err != nil {
		return errors.Wrapf(err, "json decode \"%s\"", string(data))
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
