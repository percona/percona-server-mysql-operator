package mysqlsh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/pkg/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/clusterset"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

type MysqlshExec struct {
	pod           *corev1.Pod
	containerName string
	client        clientcmd.Client
	uri           string
	stdout        io.Writer
	stderr        io.Writer
}

type ExecOptions struct {
	Pod           *corev1.Pod
	ContainerName string
	Client        clientcmd.Client
	Stdout        io.Writer
	Stderr        io.Writer
}

func (o *ExecOptions) Defaults() {
	if o.Stdout == nil {
		o.Stdout = &bytes.Buffer{}
	}
	if o.Stderr == nil {
		o.Stderr = &bytes.Buffer{}
	}
}

func (o *ExecOptions) Validate() error {
	if o.Pod == nil {
		return errors.New("pod is required")
	}
	if o.ContainerName == "" {
		return errors.New("container name is required")
	}
	if o.Client == nil {
		return errors.New("client is required")
	}
	return nil
}

func NewWithExec(uri string, opts *ExecOptions) (*MysqlshExec, error) {
	opts.Defaults()
	if err := opts.Validate(); err != nil {
		return nil, errors.Wrap(err, "validate options")
	}

	return &MysqlshExec{
		client:        opts.Client,
		pod:           opts.Pod,
		uri:           uri,
		containerName: opts.ContainerName,
		stdout:        opts.Stdout,
		stderr:        opts.Stderr,
	}, nil
}

func (m *MysqlshExec) runWithExec(ctx context.Context, cmd string) error {
	var outb, errb bytes.Buffer
	stdout := util.NewSensitiveWriter(io.MultiWriter(m.stdout, &outb), sensitiveRegexp)
	stderr := util.NewSensitiveWriter(io.MultiWriter(m.stderr, &errb), sensitiveRegexp)

	c := []string{"mysqlsh", "--js", "--no-wizard", "--uri", m.uri, "-e", cmd}
	err := m.client.Exec(ctx, m.pod, m.containerName, c, nil, stdout, stderr, false)
	if err != nil {
		return errors.Wrapf(err, "stdout: %s, stderr: %s", outb.String(), errb.String())
	}

	return nil
}

func (m *MysqlshExec) RemoveInstanceWithExec(ctx context.Context, clusterName, instance string) error {
	cmd := fmt.Sprintf("dba.getCluster('%s').removeInstance('%s', {'force': true})", clusterName, instance)

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "remove instance")
	}

	return nil
}

func (m *MysqlshExec) DoesClusterExistWithExec(ctx context.Context, clusterName string) bool {
	log := logf.FromContext(ctx)

	cmd := fmt.Sprintf("dba.getCluster('%s').status()", clusterName)
	err := m.runWithExec(ctx, cmd)
	if err != nil {
		log.Error(err, "failed to get cluster status")
	}

	return err == nil
}

func (m *MysqlshExec) ClusterStatusWithExec(ctx context.Context) (innodbcluster.Status, error) {
	status := innodbcluster.Status{}

	stdoutBuffer := bytes.Buffer{}
	stderrBuffer := bytes.Buffer{}

	c := []string{"mysqlsh", "--result-format", "json", "--js", "--uri", m.uri, "--cluster", "--", "cluster", "status"}
	err := m.client.Exec(ctx, m.pod, "mysql", c, nil, &stdoutBuffer, &stderrBuffer, false)
	if err != nil {
		sout := sensitiveRegexp.ReplaceAllString(stdoutBuffer.String(), ":*****@")
		serr := sensitiveRegexp.ReplaceAllString(stderrBuffer.String(), ":*****@")
		return status, errors.Wrapf(err, "stdout: %s, stderr: %s", sout, serr)
	}

	if err := json.Unmarshal(stdoutBuffer.Bytes(), &status); err != nil {
		return status, errors.Wrap(err, "unmarshal status")
	}

	return status, nil
}

func (m *MysqlshExec) RebootClusterFromCompleteOutageWithExec(ctx context.Context, clusterName string) error {
	cmd := fmt.Sprintf("dba.rebootClusterFromCompleteOutage('%s')", clusterName)

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "reboot cluster from complete outage")
	}

	return nil
}

func (m *MysqlshExec) SetPrimaryInstanceWithExec(ctx context.Context, clusterName, instance string) error {
	cmd := fmt.Sprintf("dba.getCluster('%s').setPrimaryInstance('%s')", clusterName, instance)

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "set primary instance")
	}

	return nil
}

func (m *MysqlshExec) Rescan80WithExec(ctx context.Context, clusterName string) error {
	cmd := fmt.Sprintf(
		"dba.getCluster('%s').rescan({'addInstances': 'auto', 'removeInstances': 'auto', 'repairMetadata': true})",
		clusterName,
	)

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "8.0: rescan cluster")
	}

	return nil
}

func (m *MysqlshExec) Rescan84WithExec(ctx context.Context, clusterName string) error {
	cmd := fmt.Sprintf(
		"dba.getCluster('%s').rescan({'addUnmanaged': true, 'removeObsolete': true, 'repairMetadata': true})",
		clusterName,
	)

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "8.4: rescan cluster")
	}

	return nil
}

func (m *MysqlshExec) CreateClusterSetWithExec(ctx context.Context, name string) error {
	cmd := fmt.Sprintf(`
var cluster = dba.getCluster()
try {
	cluster.getClusterSet()
} catch (err) {
	cluster.createClusterSet('%s')
}
`, name)
	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "create cluster set")
	}
	return nil
}

func (m *MysqlshExec) CreateReplicaClusterWithExec(
	ctx context.Context,
	clusterName, endpoint string, port int) error {
	cmd := fmt.Sprintf("dba.getCluster().getClusterSet().createReplicaCluster('%s:%d','%s',{recoveryMethod: 'clone',manualStartOnBoot: true})", endpoint, port, clusterName)
	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "create replica cluster")
	}
	return nil
}

func (m *MysqlshExec) RemoveReplicaClusterWithExec(ctx context.Context, clusterName string) error {
	cmd := fmt.Sprintf("dba.getCluster().getClusterSet().removeCluster('%s')", clusterName)
	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "remove replica cluster")
	}
	return nil
}

func (m *MysqlshExec) SetPrimaryClusterWithExec(ctx context.Context, clusterName string) error {
	cmd := fmt.Sprintf("dba.getCluster().getClusterSet().setPrimaryCluster('%s')", clusterName)
	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "set primary cluster")
	}
	return nil
}

func (m *MysqlshExec) ForcePrimaryClusterWithExec(ctx context.Context, clusterName string) error {
	cmd := fmt.Sprintf("dba.getCluster().getClusterSet().forcePrimaryCluster('%s')", clusterName)
	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "force primary cluster")
	}
	return nil
}

func (m *MysqlshExec) ClusterSetStatusWithExec(ctx context.Context) (clusterset.Status, error) {
	status := clusterset.Status{}

	stdoutBuffer := bytes.Buffer{}
	stderrBuffer := bytes.Buffer{}

	c := []string{"mysqlsh", "--result-format", "json", "--js", "--uri", m.uri, "-e", "print(dba.getCluster().getClusterSet().status())"}
	err := m.client.Exec(ctx, m.pod, m.containerName, c, nil, &stdoutBuffer, &stderrBuffer, false)
	if err != nil {
		sout := sensitiveRegexp.ReplaceAllString(stdoutBuffer.String(), ":*****@")
		serr := sensitiveRegexp.ReplaceAllString(stderrBuffer.String(), ":*****@")
		return status, errors.Wrapf(err, "stdout: %s, stderr: %s", sout, serr)
	}

	if err := json.Unmarshal(stdoutBuffer.Bytes(), &status); err != nil {
		return status, errors.Wrap(err, "unmarshal status")
	}

	return status, nil

}

var ErrEndpointUnreachable = errors.New("endpoint unreachable")

func (m *MysqlshExec) Ping(ctx context.Context) error {
	var outb, errb bytes.Buffer
	stdout := util.NewSensitiveWriter(io.MultiWriter(m.stdout, &outb), sensitiveRegexp)
	stderr := util.NewSensitiveWriter(io.MultiWriter(m.stderr, &errb), sensitiveRegexp)

	c := []string{"mysqlsh", "--sql", "--no-wizard", "--uri", m.uri, "-e", "SELECT 1"}
	if err := m.client.Exec(ctx, m.pod, m.containerName, c, nil, stdout, stderr, false); err != nil {
		if isEndpointUnreachable(err, outb.String(), errb.String()) {
			return ErrEndpointUnreachable
		}
		return errors.Wrapf(err, "ping, stdout: %s, stderr: %s", outb.String(), errb.String())
	}
	return nil
}

func isEndpointUnreachable(err error, stdout, stderr string) bool {
	msg := strings.ToLower(strings.Join([]string{err.Error(), stdout, stderr}, "\n"))
	unreachableMessages := []string{
		"mysql error (2002)",
		"mysql error (2003)",
		"mysql error (2005)",
		"can't connect to mysql server",
		"connection refused connecting to",
		"unknown mysql server host",
	}
	for _, text := range unreachableMessages {
		if strings.Contains(msg, text) {
			return true
		}
	}
	return false
}
