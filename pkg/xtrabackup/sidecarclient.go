package xtrabackup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/pkg/errors"
)

type SidecarClient struct {
	srcNode string
}

func NewSidecarClient(srcNode string) *SidecarClient {
	return &SidecarClient{srcNode: srcNode}
}

func (c *SidecarClient) port() string {
	return strconv.Itoa(mysql.SidecarHTTPPort)
}

func (c *SidecarClient) GetRunningBackupConfig(ctx context.Context) (*BackupConfig, error) {
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

func (c *SidecarClient) DeleteBackup(ctx context.Context, name string, cfg BackupConfig) error {
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
		return errors.Errorf("delete backup failed: %s", string(body))
	}
	return nil
}
