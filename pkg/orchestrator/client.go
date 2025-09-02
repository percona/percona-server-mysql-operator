package orchestrator

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"

	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
)

type orcResponse struct {
	Code    string      `json:"Code"`
	Message string      `json:"Message"`
	Details interface{} `json:"Details,omitempty"`
}

func (r *orcResponse) Error() error {
	if r.Code != "ERROR" {
		return nil
	}
	if strings.Contains(r.Message, "Unable to determine cluster name") {
		return ErrUnableToGetClusterName
	}
	if r.Message == "Unauthorized" {
		return ErrUnauthorized
	}
	if r.Message == driver.ErrBadConn.Error() {
		return ErrBadConn
	}
	if strings.Contains(r.Message, "no such host") {
		return ErrNoSuchHost
	}
	return errors.New(r.Message)
}

type InstanceKey struct {
	Hostname string `json:"Hostname"`
	Port     int32  `json:"Port"`
}

type Instance struct {
	Key                  InstanceKey   `json:"Key"`
	Alias                string        `json:"InstanceAlias"`
	MasterKey            InstanceKey   `json:"MasterKey"`
	Replicas             []InstanceKey `json:"Replicas"`
	ReadOnly             bool          `json:"ReadOnly"`
	Problems             []string      `json:"Problems"`
	IsDowntimed          bool          `json:"IsDowntimed"`
	DowntimeReason       string        `json:"DowntimeReason"`
	DowntimeOwner        string        `json:"DowntimeOwner"`
	DowntimeEndTimestamp string        `json:"DowntimeEndTimestamp"`
	ElapsedDowntime      time.Duration `json:"ElapsedDowntime"`
}

var (
	ErrEmptyResponse          = errors.New("empty response")
	ErrUnableToGetClusterName = errors.New("unable to determine cluster name")
	ErrUnauthorized           = errors.New("unauthorized")
	ErrBadConn                = errors.New("bad connection")
	ErrNoSuchHost             = errors.New("mysql host not found")
)

func exec(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, endpoint string, outb, errb *bytes.Buffer) error {
	c := []string{"curl", fmt.Sprintf("localhost:%d/%s", defaultWebPort, endpoint)}
	err := cliCmd.Exec(ctx, pod, AppName, c, nil, outb, errb, false)
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

	if err := orcResp.Error(); err != nil {
		return nil, err
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

	return orcResp.Error()
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

	return orcResp.Error()
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

	return orcResp.Error()
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

	return orcResp.Error()
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

	return orcResp.Error()
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

	return orcResp.Error()
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

	return orcResp.Error()
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

	if err := orcResp.Error(); err != nil {
		return nil, err
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

	return orcResp.Error()
}

func BeginDowntime(
	ctx context.Context,
	cliCmd clientcmd.Client,
	pod *corev1.Pod,
	host string,
	port int,
	owner string,
	reason string,
	durationSeconds int,
) error {
	url := fmt.Sprintf("api/begin-downtime/%s/%d/%s/%s/%ss", host, port, owner, reason, durationSeconds)

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

	return orcResp.Error()
}

func EndDowntime(ctx context.Context, cliCmd clientcmd.Client, pod *corev1.Pod, host string, port int) error {
	url := fmt.Sprintf("api/end-downtime/%s/%d", host, port)

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

	return orcResp.Error()
}
