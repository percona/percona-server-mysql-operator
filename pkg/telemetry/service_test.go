package telemetry

import (
	"errors"
	"testing"

	"github.com/go-openapi/runtime"
	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/telemetry/client/reporter_api"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sversion "k8s.io/apimachinery/pkg/version"
)

// MockReporterAPI is a mock implementation of the reporter_api.ClientService interface
type MockReporterAPI struct {
	GenericReportFunc func(params *reporter_api.ReporterAPIGenericReportParams, opts ...reporter_api.ClientOption) (*reporter_api.ReporterAPIGenericReportOK, error)
	SetTransportFunc  func(transport runtime.ClientTransport)
}

func (m *MockReporterAPI) ReporterAPIGenericReport(params *reporter_api.ReporterAPIGenericReportParams, opts ...reporter_api.ClientOption) (*reporter_api.ReporterAPIGenericReportOK, error) {
	if m.GenericReportFunc != nil {
		return m.GenericReportFunc(params, opts...)
	}
	return &reporter_api.ReporterAPIGenericReportOK{}, nil
}

func (m *MockReporterAPI) SetTransport(transport runtime.ClientTransport) {
	if m.SetTransportFunc != nil {
		m.SetTransportFunc(transport)
	}
}

func TestNewTelemetryService(t *testing.T) {
	tests := map[string]struct {
		endpoint    string
		expectError error
	}{
		"valid endpoint": {
			endpoint: "https://telemetry.percona.com/v1",
		},
		"invalid endpoint": {
			endpoint:    "://invalid-url",
			expectError: errors.New("invalid"),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			service, err := NewTelemetryService(tt.endpoint)

			if tt.expectError != nil {
				assert.Error(t, err)
				assert.ErrorContains(t, err, "invalid telemetry endpoint")
				assert.Nil(t, service)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, service)
				assert.NotNil(t, service.ReporterAPI)
			}
		})
	}
}

func defaultCR() *apiv1alpha1.PerconaServerMySQL {
	return &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			UID: "test-uid-123",
		},
		Spec: apiv1alpha1.PerconaServerMySQLSpec{
			CRVersion: "1.12.0",
			UpgradeOptions: apiv1alpha1.UpgradeOptions{
				Apply: "disabled",
			},
		},
		Status: apiv1alpha1.PerconaServerMySQLStatus{
			BackupVersion: "1.10.0",
			MySQL: apiv1alpha1.StatefulAppStatus{
				Version: "8.0.35",
			},
			PMMVersion: "2.41.0",
			HAProxy: apiv1alpha1.StatefulAppStatus{
				Version: "2.8.3",
			},
		},
	}
}

func defaultServerVersion() *platform.ServerVersion {
	return &platform.ServerVersion{
		Platform: platform.PlatformKubernetes,
		Info: k8sversion.Info{
			GitVersion: "v1.28.0",
		},
	}
}
