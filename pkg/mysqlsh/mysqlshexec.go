package mysqlsh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/pkg/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
)

type mysqlshExec struct {
	pod    *corev1.Pod
	client *clientcmd.Client
	uri    string
}

func NewWithExec(pod *corev1.Pod, uri string) (*mysqlshExec, error) {
	c, err := clientcmd.NewClient()
	if err != nil {
		return nil, err
	}
	return &mysqlshExec{client: c, pod: pod, uri: uri}, nil
}

func (m *mysqlshExec) runWithExec(ctx context.Context, cmd string) error {
	var errb, outb bytes.Buffer

	c := []string{"mysqlsh", "--no-wizard", "--uri", m.uri, "-e", cmd}
	err := m.client.Exec(ctx, m.pod, "mysql", c, nil, &outb, &errb, false)
	if err != nil {
		sout := sensitiveRegexp.ReplaceAllString(outb.String(), ":*****@")
		serr := sensitiveRegexp.ReplaceAllString(errb.String(), ":*****@")
		return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, sout, serr)
	}

	return nil
}

func (m *mysqlshExec) ConfigureInstanceWithExec(ctx context.Context, instance string) error {
	cmd := fmt.Sprintf(
		"dba.configureInstance('%s', {'interactive': false, 'clearReadOnly': true})",
		instance,
	)

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "configure instance")
	}

	return nil
}

func (m *mysqlshExec) AddInstanceWithExec(ctx context.Context, clusterName, instance string) error {
	opts := struct {
		Interactive    bool   `json:"interactive"`
		RecoveryMethod string `json:"recoveryMethod"`
		WaitRecovery   int    `json:"waitRecovery"`
	}{
		Interactive:    false,
		RecoveryMethod: "clone",
		WaitRecovery:   0,
	}

	o, err := json.Marshal(opts)
	if err != nil {
		return errors.Wrap(err, "marshal options")
	}

	cmd := fmt.Sprintf("dba.getCluster('%s').addInstance('%s', %s)", clusterName, instance, string(o))

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "add instance")
	}

	return nil
}

func (m *mysqlshExec) RejoinInstanceWithExec(ctx context.Context, clusterName, instance string) error {
	cmd := fmt.Sprintf("dba.getCluster('%s').rejoinInstance('%s', {'interactive': false})", clusterName, instance)

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "rejoin instance")
	}

	return nil
}

func (m *mysqlshExec) RemoveInstanceWithExec(ctx context.Context, clusterName, instance string) error {
	cmd := fmt.Sprintf("dba.getCluster('%s').removeInstance('%s', {'interactive': false, 'force': true})", clusterName, instance)

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "remove instance")
	}

	return nil
}

func (m *mysqlshExec) CreateClusterWithExec(ctx context.Context, clusterName string) error {
	cmd := fmt.Sprintf("dba.createCluster('%s', {'adoptFromGR': true})", clusterName)

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "create cluster")
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

func (m *mysqlshExec) ClusterStatusWithExec(ctx context.Context, clusterName string) (innodbcluster.Status, error) {
	status := innodbcluster.Status{}

	stdoutBuffer := bytes.Buffer{}
	stderrBuffer := bytes.Buffer{}

	c := []string{"mysqlsh", "--result-format", "json", "--uri", m.uri, "--cluster", "--", "cluster", "status"}

	err := m.client.Exec(ctx, m.pod, "mysql", c, nil, &stdoutBuffer, &stderrBuffer, false)
	if err != nil {
		sout := sensitiveRegexp.ReplaceAllString(stdoutBuffer.String(), ":*****@")
		serr := sensitiveRegexp.ReplaceAllString(stderrBuffer.String(), ":*****@")
		return status, errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", c, sout, serr)
	}

	if err := json.Unmarshal(stdoutBuffer.Bytes(), &status); err != nil {
		return status, errors.Wrap(err, "unmarshal status")
	}

	return status, nil
}

func (m *mysqlshExec) MemberStateWithExec(ctx context.Context, clusterName, instance string) (innodbcluster.MemberState, error) {
	log := logf.FromContext(ctx).WithName("InnoDBCluster").WithValues("cluster", clusterName)

	status, err := m.ClusterStatusWithExec(ctx, clusterName)
	if err != nil {
		return innodbcluster.MemberStateOffline, errors.Wrap(err, "get cluster status")
	}

	log.V(1).Info("Cluster status", "status", status)

	member, ok := status.DefaultReplicaSet.Topology[instance]
	if !ok {
		return innodbcluster.MemberStateOffline, innodbcluster.ErrMemberNotFound
	}

	return member.MemberState, nil
}

func (m *mysqlshExec) TopologyWithExec(ctx context.Context, clusterName string) (map[string]innodbcluster.Member, error) {
	status, err := m.ClusterStatusWithExec(ctx, clusterName)
	if err != nil {
		return nil, errors.Wrap(err, "get cluster status")
	}

	return status.DefaultReplicaSet.Topology, nil
}

func (m *mysqlshExec) RebootClusterFromCompleteOutageWithExec(ctx context.Context, clusterName string, rejoinInstances []string) error {
	cmd := fmt.Sprintf(
		"dba.rebootClusterFromCompleteOutage('%s', {rejoinInstances: ['%s'], removeInstances: []})",
		clusterName,
		strings.Join(rejoinInstances, ","),
	)

	if err := m.runWithExec(ctx, cmd); err != nil {
		return errors.Wrap(err, "reboot cluster from complete outage")
	}

	return nil
}
