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
		_, _ = stdout.Write(data)
	}
	return nil
}

func (f *fakeExecClient) REST() restclient.Interface { return nil }

func newReadyBinlogServerPod(cr *apiv1.PerconaServerMySQL) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: Name(cr) + "-0", Namespace: cr.Namespace},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.ContainersReady, Status: corev1.ConditionTrue}},
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

func TestSearchArgs(t *testing.T) {
	tests := map[string]struct {
		pitr               *apiv1.RestorePITRSpec
		expectedSubcommand string
		expectedArg        string
		expectedErr        string
	}{
		// the CR takes the MySQL form, the binlog server wants ISO-8601
		"date": {
			pitr:               &apiv1.RestorePITRSpec{Type: apiv1.PITRDate, Date: "2026-09-09 12:45:00"},
			expectedSubcommand: SearchByTimestampCommand,
			expectedArg:        "2026-09-09T12:45:00",
		},
		"gtid": {
			pitr:               &apiv1.RestorePITRSpec{Type: apiv1.PITRGtid, GTID: "3E11FA47-71CA-11E1-9E33-C80AA9429562:1-5"},
			expectedSubcommand: SearchByGTIDCommand,
			expectedArg:        "3E11FA47-71CA-11E1-9E33-C80AA9429562:1-5",
		},
		"empty gtid": {
			pitr:        &apiv1.RestorePITRSpec{Type: apiv1.PITRGtid},
			expectedErr: "GTID set is empty",
		},
		"malformed gtid": {
			pitr:        &apiv1.RestorePITRSpec{Type: apiv1.PITRGtid, GTID: "not-a-uuid:1-5"},
			expectedErr: "malformed GTID source",
		},
		"date in an unsupported format": {
			pitr:        &apiv1.RestorePITRSpec{Type: apiv1.PITRDate, Date: "2026-09-09 12:45:00 UTC"},
			expectedErr: `invalid pitr date "2026-09-09 12:45:00 UTC"`,
		},
		"no pitr spec": {expectedErr: "pitr spec is not set"},
		"unknown type": {
			pitr:        &apiv1.RestorePITRSpec{Type: "latest"},
			expectedErr: "unknown PITR type: latest",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			restore := &apiv1.PerconaServerMySQLRestore{
				Spec: apiv1.PerconaServerMySQLRestoreSpec{PITR: tt.pitr},
			}

			subcommand, arg, err := SearchArgs(restore)
			if tt.expectedErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedSubcommand, subcommand)
			assert.Equal(t, tt.expectedArg, arg)
		})
	}
}

func TestSearchResponseError(t *testing.T) {
	tests := map[string]struct {
		response    SearchResponse
		expectedErr string
	}{
		"success": {response: SearchResponse{Status: "success"}},
		"failure with a message": {
			response:    SearchResponse{Status: "failure", Message: "Timestamp is too old"},
			expectedErr: "binlog search failed: Timestamp is too old",
		},
		"failure without a message": {
			response:    SearchResponse{Status: "failure"},
			expectedErr: "binlog search failed with status: failure",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := tt.response.Error()
			if tt.expectedErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.expectedErr, err.Error())
		})
	}
}

func TestSearchCommands(t *testing.T) {
	cr := newTestCR("my-cluster", "test-ns")
	success := &SearchResponse{Version: 1, Status: "success", Result: []BinlogEntry{{Name: "binlog.000001"}}}

	tests := map[string]struct {
		pod              *corev1.Pod
		exec             *fakeExecClient
		subcommand       string
		arg              string
		expectedError    string
		expectedResponse *SearchResponse
	}{
		"gtid": {
			pod: newReadyBinlogServerPod(cr), exec: &fakeExecClient{response: success},
			subcommand: SearchByGTIDCommand, arg: "uuid:1-10", expectedResponse: success,
		},
		"timestamp": {
			pod: newReadyBinlogServerPod(cr), exec: &fakeExecClient{response: success},
			subcommand: SearchByTimestampCommand, arg: "2024-01-01T00:30:00", expectedResponse: success,
		},
		"pod not found": {
			exec: &fakeExecClient{}, subcommand: SearchByGTIDCommand, arg: "uuid:1", expectedError: "get binlog server pod",
		},
		"pod not ready": {
			pod:  &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: Name(cr) + "-0", Namespace: cr.Namespace}},
			exec: &fakeExecClient{}, subcommand: SearchByGTIDCommand, arg: "uuid:1", expectedError: "is not ready",
		},
		"exec error": {
			pod: newReadyBinlogServerPod(cr), exec: &fakeExecClient{execErr: fmt.Errorf("exec failed")},
			subcommand: SearchByGTIDCommand, arg: "uuid:1", expectedError: "exec binlog_server search_by_gtid_set",
		},
		"invalid response": {
			pod: newReadyBinlogServerPod(cr), exec: &fakeExecClient{},
			subcommand: SearchByGTIDCommand, arg: "uuid:1", expectedError: "unmarshal response",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cl := newSearchTestClient(t, tt.pod).Build()
			var (
				resp *SearchResponse
				err  error
			)
			if tt.subcommand == SearchByTimestampCommand {
				resp, err = SearchByTimestamp(t.Context(), cl, tt.exec, cr, nil, tt.arg)
			} else {
				resp, err = SearchByGTID(t.Context(), cl, tt.exec, cr, nil, tt.arg)
			}
			if tt.expectedError != "" {
				require.ErrorContains(t, err, tt.expectedError)
				assert.Nil(t, resp)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedResponse, resp)
			assert.Equal(t, []string{BinlogServerBinary, tt.subcommand, ConfigMountPath + "/" + ConfigKey, tt.arg}, tt.exec.capturedCmd)
		})
	}
}
