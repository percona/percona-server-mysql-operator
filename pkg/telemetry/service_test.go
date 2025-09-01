//go:generate ../../bin/mockgen -source=client/reporter_api/reporter_api_client.go -destination=mock_reporter_api_client.go -package=telemetry

package telemetry

import (
	"context"
	"errors"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sversion "k8s.io/apimachinery/pkg/version"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/telemetry/client/reporter_api"
)

var defaultUID = "b4f7e6e0-5277-4f5e-8a5b-9afc9e31d2f7"

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
			t.Setenv("TELEMETRY_SERVICE_URL", tt.endpoint)

			service, err := NewTelemetryService()

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

func TestSendReport(t *testing.T) {
	ctx := context.Background()

	tests := map[string]struct {
		setupMock func(*MockClientService)
		errorMsg  string
	}{
		"success": {
			setupMock: func(m *MockClientService) {
				m.EXPECT().ReporterAPIGenericReport(gomock.AssignableToTypeOf(&reporter_api.ReporterAPIGenericReportParams{}), gomock.Any()).
					Do(func(params *reporter_api.ReporterAPIGenericReportParams, opts ...reporter_api.ClientOption) {
						assert.NotNil(t, params.Context)
						assert.NotNil(t, params.Body)
						assert.NotNil(t, params.Body.Reports)
						assert.Len(t, params.Body.Reports, 1)

						report := params.Body.Reports[0]
						assert.NotEmpty(t, report.ID)
						assert.Equal(t, defaultUID, report.InstanceID)
						assert.NotNil(t, report.CreateTime)
						assert.NotNil(t, report.ProductFamily) // change with the assert for the final product family
						assert.Len(t, report.Metrics, 8)
					}).
					Return(&reporter_api.ReporterAPIGenericReportOK{}, nil)
			},
		},
		"error": {
			setupMock: func(m *MockClientService) {
				m.EXPECT().ReporterAPIGenericReport(gomock.AssignableToTypeOf(&reporter_api.ReporterAPIGenericReportParams{}), gomock.Any()).
					Do(func(params *reporter_api.ReporterAPIGenericReportParams, opts ...reporter_api.ClientOption) {
						assert.NotNil(t, params.Context)
						assert.NotNil(t, params.Body)
						assert.NotNil(t, params.Body.Reports)
						assert.Len(t, params.Body.Reports, 1)

						report := params.Body.Reports[0]
						assert.NotEmpty(t, report.ID)
						assert.Equal(t, defaultUID, report.InstanceID)
						assert.NotNil(t, report.CreateTime)
						assert.NotNil(t, report.ProductFamily) // change with the assert for the final product family
						assert.Len(t, report.Metrics, 8)
					}).
					Return(nil, errors.New("some error"))
			},
			errorMsg: "failed to send report to the percona telemetry service",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockReportAPI := NewMockClientService(ctrl)
			tt.setupMock(mockReportAPI)

			telemetryService := Service{ReporterAPI: mockReportAPI}

			err := telemetryService.SendReport(ctx, defaultCR(), defaultServerVersion())

			if tt.errorMsg != "" {
				assert.Error(t, err)
				assert.ErrorContains(t, err, tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSchedule(t *testing.T) {
	tests := map[string]struct {
		envValue    string
		expectedSch string
	}{
		"default schedule when env var not set": {
			expectedSch: "30 * * * *",
		},
		"custom schedule from env var": {
			envValue:    "0 12 * * *",
			expectedSch: "0 12 * * *",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if tt.envValue != "" {
				t.Setenv("TELEMETRY_SCHEDULE", tt.envValue)
			}

			s := Schedule()
			assert.Equal(t, tt.expectedSch, s)
		})
	}
}

func TestEndpoint(t *testing.T) {
	tests := map[string]struct {
		envValue      string
		expectedValue string
	}{
		"default schedule when env var not set": {
			expectedValue: "https://check-dev.percona.com/versions/v1",
		},
		"custom schedule from env var": {
			envValue:      "https://telemetry.percona.com/versions/v1",
			expectedValue: "https://telemetry.percona.com/versions/v1",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if tt.envValue != "" {
				t.Setenv("TELEMETRY_SERVICE_URL", tt.envValue)
			}

			s := endpoint()
			assert.Equal(t, tt.expectedValue, s)
		})
	}
}

func defaultCR() *apiv1alpha1.PerconaServerMySQL {
	return &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			UID: types.UID(defaultUID),
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
