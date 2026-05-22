package clusterset

import (
	"context"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type mysqlshellClusterSetManager struct {
	shell *mysqlsh.MysqlshExec
}

func NewManager(ctx context.Context, cl client.Client, cmd clientcmd.Client, pcs *apiv1.PerconaServerMySQLClusterSet) (*mysqlshellClusterSetManager, error) {
	runnerPod, err := getRunnerPod(ctx, cl, pcs)
	if err != nil {
		return nil, errors.Wrap(err, "get runner pod")
	}

	pass, err := getClusterSetAdminPassword(ctx, cl, pcs)
	if err != nil {
		return nil, errors.Wrap(err, "get clusterset admin password")
	}

	primaryCluster := pcs.PrimaryCluster()
	if primaryCluster == nil {
		return nil, errors.New("primary cluster not found")
	}

	primaryClusterURI := mysqlsh.URI(string(apiv1.UserRoot), pass, primaryCluster.Endpoints[0].Host)

	shell, err := mysqlsh.NewWithExec(cmd, runnerPod, "mysqlshell-runner", primaryClusterURI)
	if err != nil {
		return nil, errors.Wrap(err, "new mysqlsh")
	}

	return &mysqlshellClusterSetManager{shell: shell}, nil
}

func getClusterSetAdminPassword(ctx context.Context, cl client.Client, pcs *apiv1.PerconaServerMySQLClusterSet) (string, error) {
	secret := &corev1.Secret{}
	secretKeySel := pcs.Spec.CredentialsSecret
	if err := cl.Get(ctx, client.ObjectKey{Namespace: pcs.Namespace, Name: secretKeySel.Name}, secret); err != nil {
		return "", errors.Wrap(err, "get credentials secret")
	}

	password, ok := secret.Data[string(secretKeySel.Key)]
	if !ok {
		return "", errors.New("no password for clusterset admin found")
	}
	return string(password), nil
}

func getRunnerPod(ctx context.Context, cl client.Client, pcs *apiv1.PerconaServerMySQLClusterSet) (*corev1.Pod, error) {
	selector := MySQLShellRunner(pcs).Spec.Selector.MatchLabels
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

	runnerPod := runnerPods.Items[0]
	return &runnerPod, nil
}

func (m *mysqlshellClusterSetManager) CreateClusterSet(ctx context.Context, clustersetName string) error {
	if err := m.shell.CreateClusterSetWithExec(ctx, clustersetName); err != nil {
		return errors.Wrap(err, "create cluster set")
	}
	return nil
}

func (m *mysqlshellClusterSetManager) CreateReplicaCluster(ctx context.Context, cluster *apiv1.ClusterSetCluster) error {
	if err := m.shell.CreateReplicaClusterWithExec(ctx, cluster.Name, cluster.Endpoints[0].Host, int(*cluster.Endpoints[0].Port)); err != nil {
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

func (m *mysqlshellClusterSetManager) SetPrimaryCluster(ctx context.Context, clusterName string) error {
	return nil
}
