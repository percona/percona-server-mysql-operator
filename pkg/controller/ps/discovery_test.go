package ps

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	restclient "k8s.io/client-go/rest"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

type fakeDiscoverClientCmd struct {
	commands []string
	err      error
	stdout   string
}

func (f *fakeDiscoverClientCmd) Exec(_ context.Context, _ *corev1.Pod, _ string, cmd []string, _ io.Reader, stdout, _ io.Writer, _ bool) error {
	f.commands = append(f.commands, strings.Join(cmd, " "))
	if stdout != nil {
		out := f.stdout
		if out == "" {
			out = `{"Code":"OK","Message":"Instance discovered"}`
		}
		_, _ = stdout.Write([]byte(out))
	}
	return f.err
}

func (f *fakeDiscoverClientCmd) REST() restclient.Interface { return nil }

func (f *fakeDiscoverClientCmd) Config() *restclient.Config { return nil }

func bootstrappedPod(name string) corev1.Pod {
	return corev1.Pod{
		Name: name, Namespace: "ns",
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: mysql.AppName, Started: new(true)},
			},
		},
	}
}

func TestDiscoverMissingInstances(t *testing.T) {
	cr := &apiv1.PerconaServerMySQL{}
	cr.Name = "cluster1"
	cr.Namespace = "ns"

	pods := []corev1.Pod{
		bootstrappedPod("cluster1-mysql-0"),
		bootstrappedPod("cluster1-mysql-1"),
		bootstrappedPod("cluster1-mysql-2"),
	}

	t.Run("discovers the pod orchestrator does not know", func(t *testing.T) {
		instances := []*orchestrator.Instance{{Alias: "cluster1-mysql-0"}, {Alias: "cluster1-mysql-1"}}
		cliCmd := &fakeDiscoverClientCmd{}
		r := &PerconaServerMySQLReconciler{ClientCmd: cliCmd}

		require.NoError(t, r.discoverMissingInstances(t.Context(), cr, &corev1.Pod{}, instances, pods))

		require.Len(t, cliCmd.commands, 1)
		assert.Contains(t, cliCmd.commands[0], "api/discover/cluster1-mysql-2.cluster1-mysql.ns/3306")
	})

	t.Run("discovers nothing when the topology is complete", func(t *testing.T) {
		instances := []*orchestrator.Instance{
			{Alias: "cluster1-mysql-0"}, {Alias: "cluster1-mysql-1"}, {Alias: "cluster1-mysql-2"},
		}
		cliCmd := &fakeDiscoverClientCmd{}
		r := &PerconaServerMySQLReconciler{ClientCmd: cliCmd}

		require.NoError(t, r.discoverMissingInstances(t.Context(), cr, &corev1.Pod{}, instances, pods))

		assert.Empty(t, cliCmd.commands)
	})

	t.Run("skips a pod that has not bootstrapped yet", func(t *testing.T) {
		starting := corev1.Pod{
			Name: "cluster1-mysql-2", Namespace: "ns",
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: mysql.AppName, Started: new(false)},
				},
			},
		}
		instances := []*orchestrator.Instance{{Alias: "cluster1-mysql-0"}, {Alias: "cluster1-mysql-1"}}
		cliCmd := &fakeDiscoverClientCmd{}
		r := &PerconaServerMySQLReconciler{ClientCmd: cliCmd}

		require.NoError(t, r.discoverMissingInstances(t.Context(), cr, &corev1.Pod{}, instances, []corev1.Pod{starting}))

		assert.Empty(t, cliCmd.commands)
	})

	t.Run("a pod that is not up yet is left for the next reconcile", func(t *testing.T) {
		instances := []*orchestrator.Instance{{Alias: "cluster1-mysql-0"}, {Alias: "cluster1-mysql-1"}}
		cliCmd := &fakeDiscoverClientCmd{stdout: `{"Code":"ERROR","Message":"Cannot discover instance"}`}
		r := &PerconaServerMySQLReconciler{ClientCmd: cliCmd}

		assert.NoError(t, r.discoverMissingInstances(t.Context(), cr, &corev1.Pod{}, instances, pods),
			"a mysqld that is still starting must not fail the reconcile")
		assert.Len(t, cliCmd.commands, 1, "the attempt still has to be made")
	})
}
