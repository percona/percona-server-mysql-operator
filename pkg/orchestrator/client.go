package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"

	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
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
	Problems  []string      `json:"Problems"`
}

var ErrEmptyResponse = errors.New("empty response")

var ErrUnableToGetClusterName = errors.New("unable to determine cluster name")

func exec(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, endpoint string, outb, errb *bytes.Buffer) error {
	c := []string{"curl", fmt.Sprintf("localhost:%d/%s", defaultWebPort, endpoint)}
	err := cliCmd.Exec(ctx, pod, "orc", c, nil, outb, errb, false)
	if err != nil {
		return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", c, outb, errb)
	}

	return nil
}

func ClusterPrimary(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, clusterHint string) (*Instance, error) {
	url := fmt.Sprintf("api/master/%s", clusterHint)

	var res, errb bytes.Buffer
	err := exec(ctx, cliCmd, pod, url, &res, &errb)
	if err != nil {
		return nil, err
	}

	body := res.Bytes()

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

func StopReplication(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, host string, port int32) error {
	url := fmt.Sprintf("api/stop-replica/%s/%d", host, port)

	var res, errb bytes.Buffer
	err := exec(ctx, cliCmd, pod, url, &res, &errb)
	if err != nil {
		return err
	}

	orcResp := &orcResponse{}
	if err := json.Unmarshal(res.Bytes(), &orcResp); err != nil {
		return errors.Wrap(err, "json decode")
	}

	if orcResp.Code == "ERROR" {
		return errors.New(orcResp.Message)
	}

	return nil
}

func StartReplication(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, host string, port int32) error {
	url := fmt.Sprintf("api/start-replica/%s/%d", host, port)

	var res, errb bytes.Buffer
	err := exec(ctx, cliCmd, pod, url, &res, &errb)
	if err != nil {
		return err
	}

	orcResp := &orcResponse{}
	if err := json.Unmarshal(res.Bytes(), &orcResp); err != nil {
		return errors.Wrap(err, "json decode")
	}

	if orcResp.Code == "ERROR" {
		return errors.New(orcResp.Message)
	}

	return nil
}

func AddPeer(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, peer string) error {
	url := fmt.Sprintf("api/raft-add-peer/%s", peer)

	var res, errb bytes.Buffer
	err := exec(ctx, cliCmd, pod, url, &res, &errb)
	if err != nil {
		return err
	}

	body := res.Bytes()

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

func RemovePeer(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, peer string) error {
	url := fmt.Sprintf("api/raft-remove-peer/%s", peer)

	var res, errb bytes.Buffer
	err := exec(ctx, cliCmd, pod, url, &res, &errb)
	if err != nil {
		return err
	}

	body := res.Bytes()

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

func EnsureNodeIsPrimary(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, clusterHint, host string, port int) error {
	primary, err := ClusterPrimary(ctx, cliCmd, pod, clusterHint)
	if err != nil {
		return errors.Wrap(err, "get cluster primary")
	}

	if primary.Alias == host {
		return nil
	}

	url := fmt.Sprintf("api/graceful-master-takeover-auto/%s/%s/%d", clusterHint, host, port)

	var res, errb bytes.Buffer
	err = exec(ctx, cliCmd, pod, url, &res, &errb)
	if err != nil {
		return err
	}

	body := res.Bytes()

	orcResp := &orcResponse{}
	if err := json.Unmarshal(body, orcResp); err != nil {
		return errors.Wrapf(err, "json decode \"%s\"", string(body))
	}

	if orcResp.Code == "ERROR" {
		return errors.New(orcResp.Message)
	}

	return nil
}

func Discover(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, host string, port int) error {
	url := fmt.Sprintf("api/discover/%s/%d", host, port)

	var res, errb bytes.Buffer
	err := exec(ctx, cliCmd, pod, url, &res, &errb)
	if err != nil {
		return err
	}

	orcResp := new(orcResponse)
	body := res.Bytes()

	if len(body) == 0 {
		return ErrEmptyResponse
	}

	if err := json.Unmarshal(body, orcResp); err != nil {
		return errors.Wrapf(err, "json decode \"%s\"", string(body))
	}

	if orcResp.Code == "ERROR" {
		return errors.New(orcResp.Message)
	}
	return nil
}

func SetWriteable(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, host string, port int) error {
	url := fmt.Sprintf("api/set-writeable/%s/%d", host, port)

	var res, errb bytes.Buffer
	err := exec(ctx, cliCmd, pod, url, &res, &errb)
	if err != nil {
		return err
	}

	orcResp := new(orcResponse)
	body := res.Bytes()

	if len(body) == 0 {
		return ErrEmptyResponse
	}

	if err := json.Unmarshal(body, orcResp); err != nil {
		return errors.Wrapf(err, "json decode \"%s\"", string(body))
	}

	if orcResp.Code == "ERROR" {
		return errors.New(orcResp.Message)
	}
	return nil
}

func Cluster(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, clusterHint string) ([]*Instance, error) {
	url := fmt.Sprintf("api/cluster/%s", clusterHint)

	var res, errb bytes.Buffer
	err := exec(ctx, cliCmd, pod, url, &res, &errb)
	if err != nil {
		return nil, err
	}

	body := res.Bytes()
	if len(body) == 0 {
		return nil, ErrEmptyResponse
	}

	instances := []*Instance{}
	if err := json.Unmarshal(body, &instances); err == nil {
		return instances, nil
	}

	orcResp := &orcResponse{}
	if err := json.Unmarshal(body, orcResp); err != nil {
		return nil, errors.Wrap(err, "json decode")
	}

	if orcResp.Code == "ERROR" {
		if strings.Contains(orcResp.Message, "Unable to determine cluster name") {
			return nil, ErrUnableToGetClusterName
		}
		return nil, errors.New(orcResp.Message)
	}

	return instances, nil
}

func ForgetInstance(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, host string, port int) error {
	url := fmt.Sprintf("api/forget/%s/%d", host, port)

	var res, errb bytes.Buffer
	err := exec(ctx, cliCmd, pod, url, &res, &errb)
	if err != nil {
		return err
	}

	orcResp := new(orcResponse)
	body := res.Bytes()

	if len(body) == 0 {
		return ErrEmptyResponse
	}

	if err := json.Unmarshal(body, orcResp); err != nil {
		return errors.Wrapf(err, "json decode \"%s\"", string(body))
	}

	if orcResp.Code == "ERROR" {
		return errors.New(orcResp.Message)
	}
	return nil
}
