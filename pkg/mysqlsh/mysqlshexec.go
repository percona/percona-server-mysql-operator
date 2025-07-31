package mysqlsh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/pkg/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
)

type mysqlshExec struct {
	pod    *corev1.Pod
	client clientcmd.Client
	uri    string
}

func NewWithExec(cliCmd clientcmd.Client, pod *corev1.Pod, uri string) (*mysqlshExec, error) {
	return &mysqlshExec{client: cliCmd, pod: pod, uri: uri}, nil
}

func (m *mysqlshExec) runWithExec(ctx context.Context, cmd string) error {
	var errb, outb bytes.Buffer

	c := []string{"mysqlsh", "--js", "--no-wizard", "--uri", m.uri, "-e", cmd}
	err := m.client.Exec(ctx, m.pod, "mysql", c, nil, &outb, &errb, false)
	if err != nil {
		sout := sensitiveRegexp.ReplaceAllString(outb.String(), ":*****@")
		serr := sensitiveRegexp.ReplaceAllString(errb.String(), ":*****@")
		return errors.Wrapf(err, "stdout: %s, stderr: %s", sout, serr)
	}

	return nil
}

func (m *mysqlshExec) RemoveInstanceWithExec(ctx context.Context, clusterName, instance string) error {
	cmd := fmt.Sprintf("dba.getCluster('%s').removeInstance('%s', {'force': true})", clusterName, instance)

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "remove instance")
	}

	return nil
}

func (m *mysqlshExec) DoesClusterExistWithExec(ctx context.Context, clusterName string) bool {
	log := logf.FromContext(ctx)

	cmd := fmt.Sprintf("dba.getCluster('%s').status()", clusterName)
	err := m.runWithExec(ctx, cmd)
	if err != nil {
		log.Error(err, "failed to get cluster status")
	}

	return err == nil
}

func (m *mysqlshExec) ClusterStatusWithExec(ctx context.Context) (innodbcluster.Status, error) {
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

func (m *mysqlshExec) RebootClusterFromCompleteOutageWithExec(ctx context.Context, clusterName string) error {
	cmd := fmt.Sprintf("dba.rebootClusterFromCompleteOutage('%s')", clusterName)

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "reboot cluster from complete outage")
	}

	return nil
}

func (m *mysqlshExec) SetPrimaryInstanceWithExec(ctx context.Context, clusterName, instance string) error {
	cmd := fmt.Sprintf("dba.getCluster('%s').setPrimaryInstance('%s')", clusterName, instance)

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "set primary instance")
	}

	return nil
}

func (m *mysqlshExec) Rescan80WithExec(ctx context.Context, clusterName string) error {
	cmd := fmt.Sprintf(
		"dba.getCluster('%s').rescan({'addInstances': 'auto', 'removeInstances': 'auto', 'repairMetadata': true})",
		clusterName,
	)

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "8.0: rescan cluster")
	}

	return nil
}

func (m *mysqlshExec) Rescan84WithExec(ctx context.Context, clusterName string) error {
	cmd := fmt.Sprintf(
		"dba.getCluster('%s').rescan({'addUnmanaged': true, 'removeObsolete': true, 'repairMetadata': true})",
		clusterName,
	)

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "8.4: rescan cluster")
	}

	return nil
}
