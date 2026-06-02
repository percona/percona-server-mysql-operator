package manager

import (
	"context"
	"io"
	"strings"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/clusterset"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type mysqlshellClusterSetManager struct {
	shell *mysqlsh.MysqlshExec
}

type ManagerOptions struct {
	Client    client.Client
	ClientCmd clientcmd.Client
	Stdout    io.Writer
	Stderr    io.Writer
}

func New(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet, opts *ManagerOptions) (*mysqlshellClusterSetManager, error) {
	runnerPod, err := getRunnerPod(ctx, opts.Client, pcs)
	if err != nil {
		return nil, errors.Wrap(err, "get runner pod")
	}

	pass, err := getClusterSetPassword(ctx, opts.Client, pcs)
	if err != nil {
		return nil, errors.Wrap(err, "get clusterset admin password")
	}

	primaryCluster := pcs.PrimaryCluster()
	if primaryCluster == nil {
		return nil, errors.New("primary cluster not found")
	}

	for _, endpoint := range primaryCluster.Endpoints {
		uri := mysqlsh.URI(string(apiv1.UserClusterSet), pass, endpoint.Host)
		execOpts := &mysqlsh.ExecOptions{
			Pod:           runnerPod,
			ContainerName: "mysqlshell-runner",
			Client:        opts.ClientCmd,
			Stdout:        opts.Stdout,
			Stderr:        opts.Stderr,
		}
		shell, err := mysqlsh.NewWithExec(uri, execOpts)
		if err != nil {
			return nil, errors.Wrap(err, "new mysqlsh")
		}

		if err := shell.Ping(ctx); err != nil {
			if errors.Is(err, mysqlsh.ErrEndpointUnreachable) {
				continue
			}
			return nil, errors.Wrap(err, "ping")
		}

		return &mysqlshellClusterSetManager{shell: shell}, nil
	}

	return nil, errors.Wrap(mysqlsh.ErrEndpointUnreachable, "all endpoints are unreachable")

}

func getClusterSetPassword(ctx context.Context, cl client.Client, pcs *apiv1.PerconaServerMySQLClusterSet) (string, error) {
	secret := &corev1.Secret{}
	secretKeySel := pcs.Spec.CredentialsSecret
	if err := cl.Get(ctx, client.ObjectKey{Namespace: pcs.Namespace, Name: secretKeySel.Name}, secret); err != nil {
		return "", errors.Wrap(err, "get credentials secret")
	}

	key := secretKeySel.Key
	if key == "" {
		key = string(apiv1.UserClusterSet)
	}

	password, ok := secret.Data[key]
	if !ok {
		return "", errors.New("no password for clusterset found")
	}
	return string(password), nil
}

func getRunnerPod(ctx context.Context, cl client.Client, pcs *apiv1.PerconaServerMySQLClusterSet) (*corev1.Pod, error) {
	selector := clusterset.MySQLShellRunner(pcs).Spec.Selector.MatchLabels
	listOptions := &client.ListOptions{
		Namespace:     pcs.Namespace,
		LabelSelector: labels.SelectorFromSet(selector),
	}

	runnerPods := &corev1.PodList{}
	if err := cl.List(ctx, runnerPods, listOptions); err != nil {
		return nil, errors.Wrap(err, "list runner pods")
	}

	if len(runnerPods.Items) == 0 {
		return nil, errors.New("no runner pods found")
	}

	if !k8s.IsPodReady(runnerPods.Items[0]) {
		return nil, errors.New("runner pod is not ready")
	}

	runnerPod := runnerPods.Items[0]
	return &runnerPod, nil
}

func (m *mysqlshellClusterSetManager) CreateClusterSet(ctx context.Context, clustersetName string, sslMode apiv1.ClusterSetSSLMode) error {
	if err := m.shell.CreateClusterSetWithExec(ctx, clustersetName, &mysqlsh.CreateClusterSetOptions{
		SSLMode: sslMode,
	}); err != nil {
		return errors.Wrap(err, "create cluster set")
	}
	return nil
}

func (m *mysqlshellClusterSetManager) CreateReplicaCluster(ctx context.Context, cluster *apiv1.ClusterSetCluster) error {
	port := 3306
	if cluster.Endpoints[0].Port != nil {
		port = int(*cluster.Endpoints[0].Port)
	}
	if err := m.shell.CreateReplicaClusterWithExec(ctx, cluster.Name, cluster.Endpoints[0].Host, port); err != nil {
		return errors.Wrap(err, "create replica cluster")
	}
	return nil
}

func (m *mysqlshellClusterSetManager) RemoveReplicaCluster(ctx context.Context, clusterName string) error {
	if err := m.shell.RemoveReplicaClusterWithExec(ctx, clusterName); err != nil {
		return errors.Wrap(err, "remove replica cluster")
	}
	return nil
}

var AlreadyPrimaryClusterError = errors.New("already the primary cluster")

func (m *mysqlshellClusterSetManager) SetPrimaryCluster(ctx context.Context, clusterName string) error {
	if err := m.shell.SetPrimaryClusterWithExec(ctx, clusterName); err != nil {
		if strings.Contains(err.Error(), "already the PRIMARY cluster") {
			return AlreadyPrimaryClusterError
		}
		return errors.Wrap(err, "set primary cluster")
	}
	return nil
}

func (m *mysqlshellClusterSetManager) ForcePrimaryCluster(ctx context.Context, clusterName string) error {
	if err := m.shell.ForcePrimaryClusterWithExec(ctx, clusterName); err != nil {
		return errors.Wrap(err, "force primary cluster")
	}
	return nil
}

func (m *mysqlshellClusterSetManager) Status(ctx context.Context) (clusterset.Status, error) {
	return m.shell.ClusterSetStatusWithExec(ctx)
}
