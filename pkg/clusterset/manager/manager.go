package manager

import (
	"context"
	"io"
	"strings"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/clusterset"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type clusterSetManager struct {
	shell *mysqlsh.MysqlshExec

	// pcs, pass and conn back the Go-native Status() implementation, which
	// connects directly to MySQL instead of going through mysqlsh.
	pcs  *apiv1.PerconaServerMySQLClusterSet
	pass string
	conn connector
}

type ManagerOptions struct {
	Client    client.Client
	ClientCmd clientcmd.Client
	Stdout    io.Writer
	Stderr    io.Writer
}

func NewWithConnector(ctx context.Context, pcs *apiv1.PerconaServerMySQLClusterSet, opts *ManagerOptions) (*clusterSetManager, error) {
	pass, err := getClusterSetPassword(ctx, opts.Client, pcs)
	if err != nil {
		return nil, errors.Wrap(err, "get clusterset admin password")
	}

	primaryCluster := pcs.PrimaryCluster()
	if primaryCluster == nil {
		return nil, errors.New("primary cluster not found")
	}

	connector := sqlConnector{}
	_, err = connector.connect(ctx, primaryCluster.Endpoints, pass)
	if err != nil {
		return nil, errors.Wrap(err, "connect to primary cluster")
	}

	return &clusterSetManager{
		pcs:  pcs,
		pass: pass,
		conn: connector,
	}, nil
}

func NewWithShellExec(
	ctx context.Context,
	pcs *apiv1.PerconaServerMySQLClusterSet,
	execPod *corev1.Pod,
	opts *ManagerOptions,
) (*clusterSetManager, error) {
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
			Pod:           execPod,
			ContainerName: "mysqlshell",
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

		return &clusterSetManager{
			shell: shell,
			pcs:   pcs,
			pass:  pass,
			conn:  sqlConnector{},
		}, nil
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

func (m *clusterSetManager) CreateClusterSet(ctx context.Context, clustersetName string, sslMode apiv1.ClusterSetSSLMode) error {
	if err := m.shell.CreateClusterSetWithExec(ctx, clustersetName, &mysqlsh.CreateClusterSetOptions{
		SSLMode: sslMode,
	}); err != nil {
		return errors.Wrap(err, "create cluster set")
	}
	return nil
}

func (m *clusterSetManager) CreateReplicaCluster(ctx context.Context, cluster *apiv1.ClusterSetCluster, recoveryMethod string) error {
	if err := m.shell.CreateReplicaClusterWithExec(ctx, cluster.InnoDBClusterName, recoveryMethod, cluster.Endpoints[0].Host, cluster.Endpoints[0].GetPort()); err != nil {
		return errors.Wrap(err, "create replica cluster")
	}
	return nil
}

func (m *clusterSetManager) RemoveReplicaCluster(ctx context.Context, clusterName string, force bool) error {
	if err := m.shell.RemoveReplicaClusterWithExec(ctx, clusterName, force); err != nil {
		return errors.Wrap(err, "remove replica cluster")
	}
	return nil
}

var AlreadyPrimaryClusterError = errors.New("already the primary cluster")

func (m *clusterSetManager) SetPrimaryCluster(ctx context.Context, clusterName string) error {
	if err := m.shell.SetPrimaryClusterWithExec(ctx, clusterName); err != nil {
		if strings.Contains(err.Error(), "already the PRIMARY cluster") {
			return AlreadyPrimaryClusterError
		}
		return errors.Wrap(err, "set primary cluster")
	}
	return nil
}

func (m *clusterSetManager) ForcePrimaryCluster(ctx context.Context, clusterName string) error {
	if err := m.shell.ForcePrimaryClusterWithExec(ctx, clusterName); err != nil {
		return errors.Wrap(err, "force primary cluster")
	}
	return nil
}

func (m *clusterSetManager) Status(ctx context.Context) (clusterset.Status, error) {
	return computeStatus(ctx, m.pcs, m.pass, m.conn)
}
