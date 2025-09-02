package telemetry

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	telemetryclient "github.com/percona/percona-server-mysql-operator/pkg/telemetry/client"
	"github.com/percona/percona-server-mysql-operator/pkg/telemetry/client/models"
	"github.com/percona/percona-server-mysql-operator/pkg/telemetry/client/reporter_api"
)

const (
	metricBackupVersion       = "backup_version"
	metricDatabaseVersion     = "database_version"
	metricUpgradeOptionsApply = "upgrade_options_apply"
	metricOperatorVersion     = "operator_version"
	metricPMMVersion          = "pmm_version"
	metricHAProxyVersion      = "haproxy_version"
	metricPlatform            = "platform"
	metricKubernetesVersion   = "kubernetes_version"
)

// Service defines the properties of the telemetry service.
type Service struct {
	reporterAPI reporter_api.ClientService
}

// NewTelemetryService creates a new Service.
func NewTelemetryService() (*Service, error) {
	requestURL, err := url.Parse(serviceURL())
	if err != nil {
		return nil, fmt.Errorf("invalid telemetry url: %w", err)
	}

	telemetryClient := telemetryclient.NewHTTPClientWithConfig(nil, &telemetryclient.TransportConfig{
		Host:     requestURL.Host,
		BasePath: requestURL.Path,
		Schemes:  []string{requestURL.Scheme},
	})

	return &Service{
		reporterAPI: telemetryClient.ReporterAPI,
	}, nil
}

// SendReport sends the report with the custom metric data to the telemetry service.
func (s Service) SendReport(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, serverVersion *platform.ServerVersion) error {
	logger := logf.FromContext(ctx)

	report := createReport(cr, serverVersion)

	logger.Info("generated telemetry report", "id", report.ID)

	params := &reporter_api.ReporterAPIGenericReportParams{
		Context: ctx,
		Body: &models.V1ReportRequest{
			Reports: []*models.Genericv1GenericReport{&report},
		},
	}

	// Since errors are handled internally in ReporterAPIGenericReport, processing the ReporterAPIGenericReportOK is redundant for now.
	_, err := s.reporterAPI.ReporterAPIGenericReport(params)
	if err != nil {
		return errors.Wrap(err, "failed to send report to the percona telemetry service")
	}

	return nil
}

func createReport(cr *apiv1alpha1.PerconaServerMySQL, serverVersion *platform.ServerVersion) models.Genericv1GenericReport {
	metrics := []*models.GenericReportMetric{
		{
			Key:   metricUpgradeOptionsApply,
			Value: cr.Spec.UpgradeOptions.Apply,
		},
		{
			Key:   metricBackupVersion,
			Value: cr.Status.BackupVersion,
		},
		{
			Key:   metricDatabaseVersion,
			Value: cr.Status.MySQL.Version,
		},
		{
			Key:   metricKubernetesVersion,
			Value: serverVersion.Info.GitVersion,
		},
		{
			Key:   metricOperatorVersion,
			Value: cr.Spec.CRVersion,
		},
		{
			Key:   metricPlatform,
			Value: string(serverVersion.Platform),
		},
		{
			Key:   metricPMMVersion,
			Value: cr.Status.PMMVersion,
		},
		{
			Key:   metricHAProxyVersion,
			Value: cr.Status.HAProxy.Version,
		},
	}

	return models.Genericv1GenericReport{
		ID:            uuid.NewString(),
		CreateTime:    strfmt.DateTime(time.Now()),
		ProductFamily: models.V1ProductFamilyPRODUCTFAMILYOPERATORPS.Pointer(),
		InstanceID:    string(cr.GetUID()),
		Metrics:       metrics,
	}
}

// Schedule returns the schedule for sending telemetry requests.
func Schedule() string {
	sch, found := os.LookupEnv("TELEMETRY_SCHEDULE")
	if !found {
		sch = "30 * * * *"
	}
	return sch
}

// serviceURL returns the url for sending telemetry requests.
func serviceURL() string {
	sch, found := os.LookupEnv("TELEMETRY_SERVICE_URL")
	if !found {
		sch = "https://check-dev.percona.com" // to change this with the production url as default.
	}
	return sch
}
