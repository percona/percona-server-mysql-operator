package binlogserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
)

type fakeExecClient struct {
	response    *SearchResponse
	execErr     error
	capturedCmd []string
}

var _ clientcmd.Client = (*fakeExecClient)(nil)

func (f *fakeExecClient) Exec(_ context.Context, _ *corev1.Pod, _ string, cmd []string, _ io.Reader, stdout, _ io.Writer, _ bool) error {
	f.capturedCmd = cmd
	if f.execErr != nil {
		return f.execErr
	}
	if stdout != nil && f.response != nil {
		data, _ := json.Marshal(f.response)
		stdout.Write(data)
	}
	return nil
}

func (f *fakeExecClient) REST() restclient.Interface {
	return nil
}

func newReadyBinlogServerPod(cr *apiv1.PerconaServerMySQL) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      Name(cr) + "-0",
			Namespace: cr.Namespace,
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{
					Type:   corev1.ContainersReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
}

func newSearchTestClient(t *testing.T, pod *corev1.Pod) *fake.ClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))
	cb := fake.NewClientBuilder().WithScheme(scheme)
	if pod != nil {
		cb = cb.WithObjects(pod)
	}
	return cb
}

func TestSearchByGTID(t *testing.T) {
	cr := newTestCR("my-cluster", "test-ns")

	successResponse := &SearchResponse{
		Version: 1,
		Status:  "OK",
		Result: []BinlogEntry{
			{
				Name:          "binlog.000001",
				PreviousGTIDs: "00000000-0000-0000-0000-000000000000:1-10",
				AddedGTIDs:    "00000000-0000-0000-0000-000000000000:11",
			},
		},
	}

	tests := map[string]struct {
		pod              *corev1.Pod
		cliCmd           clientcmd.Client
		gtidSet          string
		expectedResponse *SearchResponse
		expectedError    string
	}{
		"success": {
			pod:              newReadyBinlogServerPod(cr),
			cliCmd:           &fakeExecClient{response: successResponse},
			gtidSet:          "00000000-0000-0000-0000-000000000000:1-10",
			expectedResponse: successResponse,
		},
		"pod not found": {
			cliCmd:        &fakeExecClient{},
			gtidSet:       "some-gtid",
			expectedError: "get binlog server pod",
		},
		"pod not ready": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      Name(cr) + "-0",
					Namespace: cr.Namespace,
				},
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			},
			cliCmd:        &fakeExecClient{},
			gtidSet:       "some-gtid",
			expectedError: "is not ready",
		},
		"exec error": {
			pod:           newReadyBinlogServerPod(cr),
			cliCmd:        &fakeExecClient{execErr: fmt.Errorf("exec failed")},
			gtidSet:       "some-gtid",
			expectedError: "exec binlog_server search_by_gtid_set",
		},
		"invalid json response": {
			pod:           newReadyBinlogServerPod(cr),
			cliCmd:        &fakeExecClient{response: nil},
			gtidSet:       "some-gtid",
			expectedError: "unmarshal response",
		},
	}

	configPath := configMountPath + "/" + ConfigKey

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cl := newSearchTestClient(t, tt.pod).Build()

			execClient, _ := tt.cliCmd.(*fakeExecClient)
			resp, err := SearchByGTID(t.Context(), cl, tt.cliCmd, cr, tt.gtidSet)
			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				assert.Nil(t, resp)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedResponse, resp)
			assert.Equal(t, []string{binlogServerBinary, "search_by_gtid_set", configPath, tt.gtidSet}, execClient.capturedCmd)
		})
	}
}

func TestSearchByTimestamp(t *testing.T) {
	cr := newTestCR("my-cluster", "test-ns")

	successResponse := &SearchResponse{
		Version: 1,
		Status:  "OK",
		Result: []BinlogEntry{
			{
				Name:         "binlog.000002",
				MinTimestamp: "2024-01-01 00:00:00",
				MaxTimestamp: "2024-01-01 01:00:00",
			},
		},
	}

	tests := map[string]struct {
		pod              *corev1.Pod
		cliCmd           clientcmd.Client
		timestamp        string
		expectedResponse *SearchResponse
		expectedError    string
	}{
		"success": {
			pod:              newReadyBinlogServerPod(cr),
			cliCmd:           &fakeExecClient{response: successResponse},
			timestamp:        "2024-01-01 00:30:00",
			expectedResponse: successResponse,
		},
		"pod not found": {
			cliCmd:        &fakeExecClient{},
			timestamp:     "2024-01-01 00:30:00",
			expectedError: "get binlog server pod",
		},
		"pod not ready": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      Name(cr) + "-0",
					Namespace: cr.Namespace,
				},
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			},
			cliCmd:        &fakeExecClient{},
			timestamp:     "2024-01-01 00:30:00",
			expectedError: "is not ready",
		},
		"exec error": {
			pod:           newReadyBinlogServerPod(cr),
			cliCmd:        &fakeExecClient{execErr: fmt.Errorf("exec failed")},
			timestamp:     "2024-01-01 00:30:00",
			expectedError: "exec binlog_server search_by_timestamp",
		},
		"invalid json response": {
			pod:           newReadyBinlogServerPod(cr),
			cliCmd:        &fakeExecClient{response: nil},
			timestamp:     "2024-01-01 00:30:00",
			expectedError: "unmarshal response",
		},
	}

	configPath := configMountPath + "/" + ConfigKey

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cl := newSearchTestClient(t, tt.pod).Build()

			execClient, _ := tt.cliCmd.(*fakeExecClient)
			resp, err := SearchByTimestamp(t.Context(), cl, tt.cliCmd, cr, tt.timestamp)
			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				assert.Nil(t, resp)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedResponse, resp)
			assert.Equal(t, []string{binlogServerBinary, "search_by_timestamp", configPath, tt.timestamp}, execClient.capturedCmd)
		})
	}
}
