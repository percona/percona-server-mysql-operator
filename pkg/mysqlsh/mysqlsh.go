package mysqlsh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	k8sexec "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/percona/percona-server-mysql-operator/pkg/innodbcluster"
)

type mysqlsh struct {
	uri  string
	exec k8sexec.Interface
}

var ErrMetadataExistsButGRNotActive = errors.New("MYSQLSH 51314: metadata exists, instance belongs to that metadata, but GR is not active")

var sensitiveRegexp = regexp.MustCompile(":.*@")

const defaultIPAllowList = "127.0.0.1/8,::1/128"

func New(e k8sexec.Interface, uri string) *mysqlsh {
	return &mysqlsh{exec: e, uri: uri}
}

func (m *mysqlsh) run(ctx context.Context, cmd string) error {
	var errb, outb bytes.Buffer

	args := []string{"--uri", m.uri, "-e", cmd}

	c := m.exec.CommandContext(ctx, "mysqlsh", args...)
	c.SetStdout(&outb)
	c.SetStderr(&errb)

	if err := c.Run(); err != nil {
		sout := sensitiveRegexp.ReplaceAllString(outb.String(), ":*****@")
		serr := sensitiveRegexp.ReplaceAllString(errb.String(), ":*****@")
		return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, sout, serr)
	}

	return nil
}

func (m *mysqlsh) ConfigureInstance(ctx context.Context, instance string) error {
	cmd := fmt.Sprintf(
		"dba.configureInstance('%s', {'interactive': false, 'clearReadOnly': true})",
		instance,
	)

	if err := m.run(ctx, cmd); err != nil {
		return errors.Wrap(err, "configure instance")
	}

	return nil
}

func (m *mysqlsh) AddInstance(ctx context.Context, clusterName, instance, podIp string) error {
	opts := struct {
		Interactive    bool   `json:"interactive"`
		RecoveryMethod string `json:"recoveryMethod"`
		WaitRecovery   int    `json:"waitRecovery"`
		IpAllowList    string `json:"ipAllowList"`
	}{
		Interactive:    false,
		RecoveryMethod: "clone",
		WaitRecovery:   0,
		IpAllowList:    fmt.Sprintf("%s,%s/8", defaultIPAllowList, podIp),
	}

	o, err := json.Marshal(opts)
	if err != nil {
		return errors.Wrap(err, "marshal options")
	}

	cmd := fmt.Sprintf("dba.getCluster('%s').addInstance('%s', %s)", clusterName, instance, string(o))

	if err := m.run(ctx, cmd); err != nil {
		return errors.Wrap(err, "add instance")
	}

	return nil
}

func (m *mysqlsh) RejoinInstance(ctx context.Context, clusterName, instance string) error {
	cmd := fmt.Sprintf("dba.getCluster('%s').rejoinInstance('%s', {'interactive': false})", clusterName, instance)

	if err := m.run(ctx, cmd); err != nil {
		return errors.Wrap(err, "rejoin instance")
	}

	return nil
}

func (m *mysqlsh) RemoveInstance(ctx context.Context, clusterName, instance string) error {
	cmd := fmt.Sprintf("dba.getCluster('%s').removeInstance('%s', {'interactive': false, 'force': true})", clusterName, instance)

	if err := m.run(ctx, cmd); err != nil {
		return errors.Wrap(err, "remove instance")
	}

	return nil
}

func (m *mysqlsh) CreateCluster(ctx context.Context, clusterName, podIp string) error {
	cmd := fmt.Sprintf("dba.createCluster('%s', {'ipAllowList': '%s,%s/8'})", clusterName, defaultIPAllowList, podIp)

	if err := m.run(ctx, cmd); err != nil {
		return errors.Wrap(err, "create cluster")
	}

	return nil
}

func (m *mysqlsh) DoesClusterExist(ctx context.Context, clusterName string) (bool, error) {
	cmd := fmt.Sprintf("dba.getCluster('%s').status()", clusterName)
	err := m.run(ctx, cmd)
	if err == nil {
		return true, err
	}

	if strings.Contains(err.Error(), "MYSQLSH 51314") {
		return true, ErrMetadataExistsButGRNotActive
	}

	return false, nil
}

func (m *mysqlsh) ClusterStatus(ctx context.Context, clusterName string) (innodbcluster.Status, error) {
	var errb, outb bytes.Buffer

	args := []string{"--result-format", "json", "--uri", m.uri, "--cluster", "--", "cluster", "status"}

	c := m.exec.CommandContext(ctx, "mysqlsh", args...)
	c.SetStdout(&outb)
	c.SetStderr(&errb)

	status := innodbcluster.Status{}

	if err := c.Run(); err != nil {
		sout := sensitiveRegexp.ReplaceAllString(outb.String(), ":*****@")
		serr := sensitiveRegexp.ReplaceAllString(errb.String(), ":*****@")
		return status, errors.Wrapf(err, "run Cluster.status(), stdout: %s, stderr: %s", sout, serr)
	}

	if err := json.Unmarshal(outb.Bytes(), &status); err != nil {
		return status, errors.Wrap(err, "unmarshal status")
	}

	return status, nil
}

func (m *mysqlsh) MemberState(ctx context.Context, clusterName, instance string) (innodbcluster.MemberState, error) {
	l := log.FromContext(ctx).WithName("InnoDBCluster").WithValues("cluster", clusterName)

	status, err := m.ClusterStatus(ctx, clusterName)
	if err != nil {
		return innodbcluster.MemberStateOffline, errors.Wrap(err, "get cluster status")
	}

	l.V(1).Info("Cluster status", "status", status)

	member, ok := status.DefaultReplicaSet.Topology[instance]
	if !ok {
		return innodbcluster.MemberStateOffline, innodbcluster.ErrMemberNotFound
	}

	return member.MemberState, nil
}

func (m *mysqlsh) Topology(ctx context.Context, clusterName string) (map[string]innodbcluster.Member, error) {
	status, err := m.ClusterStatus(ctx, clusterName)
	if err != nil {
		return nil, errors.Wrap(err, "get cluster status")
	}

	return status.DefaultReplicaSet.Topology, nil
}

func (m *mysqlsh) RebootClusterFromCompleteOutage(ctx context.Context, clusterName string, rejoinInstances []string) error {
	cmd := fmt.Sprintf(
		"dba.rebootClusterFromCompleteOutage('%s', {rejoinInstances: ['%s']})",
		clusterName,
		strings.Join(rejoinInstances, ","),
	)

	if err := m.run(ctx, cmd); err != nil {
		return errors.Wrap(err, "reboot cluster from complete outage")
	}

	return nil
}
