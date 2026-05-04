package binlogserver

import (
	"bytes"
	"context"
	"encoding/json"
	"path"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

const binlogServerBinary = "/usr/bin/binlog_server"

type SearchResponse struct {
	Version int           `json:"version"`
	Status  string        `json:"status"`
	Result  []BinlogEntry `json:"result"`
}

type BinlogEntry struct {
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	URI           string `json:"uri"`
	PreviousGTIDs string `json:"previous_gtids"`
	AddedGTIDs    string `json:"added_gtids"`
	MinTimestamp  string `json:"min_timestamp"`
	MaxTimestamp  string `json:"max_timestamp"`
}

func SearchByGTID(ctx context.Context, cl client.Client, cliCmd clientcmd.Client, cr *apiv1.PerconaServerMySQL, gtidSet string) (*SearchResponse, error) {
	return execSearch(ctx, cl, cliCmd, cr, "search_by_gtid_set", gtidSet)
}

func SearchByTimestamp(ctx context.Context, cl client.Client, cliCmd clientcmd.Client, cr *apiv1.PerconaServerMySQL, timestamp string) (*SearchResponse, error) {
	return execSearch(ctx, cl, cliCmd, cr, "search_by_timestamp", timestamp)
}

func execSearch(ctx context.Context, cl client.Client, cliCmd clientcmd.Client, cr *apiv1.PerconaServerMySQL, subcommand, arg string) (*SearchResponse, error) {
	pod, err := getBinlogServerPod(ctx, cl, cr)
	if err != nil {
		return nil, errors.Wrap(err, "get binlog server pod")
	}

	configPath := path.Join(configMountPath, ConfigKey)
	cmd := []string{binlogServerBinary, subcommand, configPath, arg}

	var stdout, stderr bytes.Buffer
	if err := cliCmd.Exec(ctx, pod, AppName, cmd, nil, &stdout, &stderr, false); err != nil {
		return nil, errors.Wrapf(err, "exec binlog_server %s: stdout: %s stderr: %s", subcommand, stdout.String(), stderr.String())
	}

	var resp SearchResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, errors.Wrapf(err, "unmarshal response: %s", stdout.String())
	}

	return &resp, nil
}

func getBinlogServerPod(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQL) (*corev1.Pod, error) {
	nn := types.NamespacedName{
		Namespace: cr.Namespace,
		Name:      Name(cr) + "-0",
	}

	pod := &corev1.Pod{}
	if err := cl.Get(ctx, nn, pod); err != nil {
		return nil, errors.Wrapf(err, "get pod %s", nn)
	}

	if !k8s.IsPodReady(*pod) {
		return nil, errors.Errorf("binlog server pod %s is not ready", nn)
	}

	return pod, nil
}
