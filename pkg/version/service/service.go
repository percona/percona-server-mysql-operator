package service

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	vsclient "github.com/percona/percona-server-mysql-operator/pkg/version/service/client"
	"github.com/percona/percona-server-mysql-operator/pkg/version/service/client/models"
	"github.com/percona/percona-server-mysql-operator/pkg/version/service/client/version_service"
)

const (
	productName     = "ps-operator"
	defaultEndpoint = "https://check.percona.com"
)

func GetDefaultVersionServiceEndpoint() string {
	if endpoint := os.Getenv("PERCONA_VS_FALLBACK_URI"); len(endpoint) > 0 {
		return endpoint
	}

	return defaultEndpoint
}

func GetVersion(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, endpoint string, serverVersion *platform.ServerVersion) (DepVersion, error) {
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return DepVersion{}, errors.Wrap(err, "url parse")
	}

	client := vsclient.NewHTTPClientWithConfig(nil, &vsclient.TransportConfig{
		Host:     requestURL.Host,
		BasePath: requestURL.Path,
		Schemes:  []string{requestURL.Scheme},
	})

	timeout := 10 * time.Second
	crUID := string(cr.GetUID())
	platformStr := string(serverVersion.Platform)

	applyParams := &version_service.VersionServiceApplyParams{
		Apply:             cr.Spec.UpgradeOptions.Apply,
		BackupVersion:     &cr.Status.BackupVersion,
		CustomResourceUID: &crUID,
		DatabaseVersion:   &cr.Status.MySQL.Version,
		KubeVersion:       &serverVersion.Info.GitVersion,
		OperatorVersion:   cr.Spec.CRVersion,
		Platform:          &platformStr,
		Product:           productName,
		Context:           ctx,
		HTTPClient:        &http.Client{Timeout: timeout},
		PmmVersion:        &cr.Status.PMMVersion,
		HaproxyVersion:    &cr.Status.HAProxy.Version,
		ToolkitVersion:    &cr.Status.ToolkitVersion,
	}
	applyParams = applyParams.WithTimeout(timeout)

	resp, err := client.VersionService.VersionServiceApply(applyParams)
	if err != nil {
		return DepVersion{}, errors.Wrap(err, "version service apply")
	}

	if resp.Payload == nil || len(resp.Payload.Versions) == 0 {
		return DepVersion{}, nil
	}

	matrix := resp.Payload.Versions[0].Matrix

	pmmVersion, err := getVersion(matrix.Pmm)
	if err != nil {
		return DepVersion{}, errors.Wrap(err, "get pmm version")
	}

	backupVersion, err := getVersion(matrix.Backup)
	if err != nil {
		return DepVersion{}, errors.Wrap(err, "get backup version")
	}

	psVersion, err := getVersion(matrix.Mysql)
	if err != nil {
		return DepVersion{}, errors.Wrap(err, "get mysql version")
	}

	orchestratorVersion, err := getVersion(matrix.Orchestrator)
	if err != nil {
		return DepVersion{}, errors.Wrap(err, "get orchestrator version")
	}

	routerVersion, err := getVersion(matrix.Router)
	if err != nil {
		return DepVersion{}, errors.Wrap(err, "get router version")
	}

	haproxyVersion, err := getVersion(matrix.Haproxy)
	if err != nil {
		return DepVersion{}, errors.Wrap(err, "get haproxy version")
	}

	toolkitVersion, err := getVersion(matrix.Toolkit)
	if err != nil {
		return DepVersion{}, errors.Wrap(err, "get toolkit version")
	}

	dv := DepVersion{
		PSImage:             matrix.Mysql[psVersion].ImagePath,
		PSVersion:           psVersion,
		BackupImage:         matrix.Backup[backupVersion].ImagePath,
		BackupVersion:       backupVersion,
		OrchestratorImage:   matrix.Orchestrator[orchestratorVersion].ImagePath,
		OrchestratorVersion: orchestratorVersion,
		RouterImage:         matrix.Router[routerVersion].ImagePath,
		RouterVersion:       routerVersion,
		PMMImage:            matrix.Pmm[pmmVersion].ImagePath,
		PMMVersion:          pmmVersion,
		HAProxyImage:        matrix.Haproxy[haproxyVersion].ImagePath,
		HAProxyVersion:      haproxyVersion,
		ToolkitImage:        matrix.Toolkit[toolkitVersion].ImagePath,
		ToolkitVersion:      toolkitVersion,
	}
	return dv, nil
}

type DepVersion struct {
	PSImage             string `json:"psImage,omitempty"`
	PSVersion           string `json:"psVersion,omitempty"`
	BackupImage         string `json:"backupImage,omitempty"`
	BackupVersion       string `json:"backupVersion,omitempty"`
	OrchestratorImage   string `json:"orchestratorImage,omitempty"`
	OrchestratorVersion string `json:"orchestratorVersion,omitempty"`
	RouterImage         string `json:"routerImage,omitempty"`
	RouterVersion       string `json:"routerVersion,omitempty"`
	PMMImage            string `json:"pmmImage,omitempty"`
	PMMVersion          string `json:"pmmVersion,omitempty"`
	HAProxyImage        string `json:"haproxyImage,omitempty"`
	HAProxyVersion      string `json:"haproxyVersion,omitempty"`
	ToolkitImage        string `json:"toolkitImage,omitempty"`
	ToolkitVersion      string `json:"toolkitVersion,omitempty"`
}

func getVersion(versions map[string]models.VersionVersion) (string, error) {
	if len(versions) != 1 {
		return "", errors.New("response has multiple or zero versions")
	}

	for k := range versions {
		return k, nil
	}
	return "", nil
}
