package xtrabackup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/pkg/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

type SidecarClient interface {
	GetRunningBackupConfig(ctx context.Context) (*BackupConfig, error)
	DeleteBackup(ctx context.Context, name string, cfg BackupConfig) error
	GetCheckpointInfo(ctx context.Context, cfg BackupConfig) (*CheckpointInfo, error)
}

type NewSidecarClientFunc func(srcNode string) SidecarClient

type sidecarClient struct {
	srcNode string
}

func NewSidecarClient(srcNode string) SidecarClient {
	return &sidecarClient{srcNode: srcNode}
}

func (c *sidecarClient) port() string {
	return strconv.Itoa(mysql.SidecarHTTPPort)
}

func (c *sidecarClient) GetRunningBackupConfig(ctx context.Context) (*BackupConfig, error) {
	sidecarURL := url.URL{
		Host:   c.srcNode + ":" + c.port(),
		Scheme: "http",
		Path:   "/backup/",
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sidecarURL.String(), nil)
	if err != nil {
		return nil, errors.Wrap(err, "create http request")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "get backup")
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "read response body")
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, errors.Errorf("get backup failed: %s %d", string(data), resp.StatusCode)
	}

	backupConf := new(BackupConfig)
	if err := json.Unmarshal(data, backupConf); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal")
	}
	return backupConf, nil
}

func (c *sidecarClient) DeleteBackup(ctx context.Context, name string, cfg BackupConfig) error {
	sidecarURL := url.URL{
		Host:   c.srcNode + ":" + c.port(),
		Scheme: "http",
		Path:   "/backup/" + name,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return errors.Wrap(err, "marshal backup config")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, sidecarURL.String(), bytes.NewReader(data))
	if err != nil {
		return errors.Wrap(err, "create http request")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "delete backup")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return errors.Wrap(err, "read response body")
		}
		return errors.Errorf("delete backup failed: %s (status: %d)", string(body), resp.StatusCode)
	}
	return nil
}

func (c *sidecarClient) GetCheckpointInfo(ctx context.Context, cfg BackupConfig) (*CheckpointInfo, error) {
	log := logf.FromContext(ctx).WithName("GetCheckpointInfo")
	sidecarURL := url.URL{
		Host:   c.srcNode + ":" + c.port(),
		Scheme: "http",
		Path:   "/backup/checkpoint-info",
	}
	reqData, err := json.Marshal(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "marshal backup config")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sidecarURL.String(), bytes.NewReader(reqData))
	if err != nil {
		return nil, errors.Wrap(err, "create http request")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "get checkpoint info")
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Error(err, "failed to close response body")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, errors.Wrap(err, "read response body")
		}
		return nil, errors.Errorf("get checkpoint info failed: %s (status: %d)", string(body), resp.StatusCode)
	}

	info := new(CheckpointInfo)
	if err := json.NewDecoder(resp.Body).Decode(info); err != nil {
		return nil, errors.Wrap(err, "decode checkpoint info")
	}
	return info, nil
}
