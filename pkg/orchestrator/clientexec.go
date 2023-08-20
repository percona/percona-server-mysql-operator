package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"

	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
)

// exec is a utility function to execute an HTTP GET request inside a container using the provided client.
func exec(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, endpoint string, outb, errb *bytes.Buffer) error {
	c := []string{"curl", fmt.Sprintf("localhost:%d/%s", defaultWebPort, endpoint)}
	err := cliCmd.Exec(ctx, pod, "orc", c, nil, outb, errb, false)
	if err != nil {
		return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", c, outb, errb)
	}

	return nil
}

// ClusterPrimaryExec retrieves the primary instance of a cluster using the Orchestrator API within a container.
func ClusterPrimaryExec(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, clusterHint string) (*Instance, error) {
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

// StopReplicationExec stops replication on the specified host and port using the Orchestrator API within a container.
func StopReplicationExec(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, host string, port int32) error {
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

// StartReplicationExec starts replication on the specified host and port using the Orchestrator API within a container.
func StartReplicationExec(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, host string, port int32) error {
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

// AddPeerExec adds a new peer to a Raft cluster using the Orchestrator API within a container.
func AddPeerExec(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, peer string) error {
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

// RemovePeerExec removes a peer from a Raft cluster using the Orchestrator API within a container.
func RemovePeerExec(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, peer string) error {
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

// EnsureNodeIsPrimaryExec ensures that the specified node becomes the primary of the cluster using the Orchestrator API within a container.
func EnsureNodeIsPrimaryExec(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, clusterHint, host string, port int) error {
	primary, err := ClusterPrimaryExec(ctx, cliCmd, pod, clusterHint)
	if err != nil {
		return errors.Wrap(err, "get cluster primary")
	}

	if primary.Alias == host {
		return nil
	}

	// /api/graceful-master-takeover-auto/cluster1.default/cluster1-mysql-0/3306
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
	// if err := json.NewDecoder(res.Bytes()).Decode(orcResp); err != nil {
	// 	return errors.Wrap(err, "json decode")
	// }

	if orcResp.Code == "ERROR" {
		return errors.New(orcResp.Message)
	}

	return nil
}

// DiscoverExec triggers discovery of a node using the Orchestrator API within a container.
func DiscoverExec(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, host string, port int) error {
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
