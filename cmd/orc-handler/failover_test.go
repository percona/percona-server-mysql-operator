package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

func instance(host string, master string, logFile string, logPos int64, problems ...string) *orchestrator.Instance {
	return &orchestrator.Instance{
		Key:                   orchestrator.InstanceKey{Hostname: host, Port: 3306},
		MasterKey:             orchestrator.InstanceKey{Hostname: master, Port: 3306},
		Problems:              problems,
		ExecBinlogCoordinates: orchestrator.BinlogCoordinates{LogFile: logFile, LogPos: logPos},
	}
}

func TestReplicasOf(t *testing.T) {
	tests := map[string]struct {
		instances []*orchestrator.Instance
		host      string
		want      []string
	}{
		"replicas of the primary": {
			instances: []*orchestrator.Instance{
				instance("mysql-0", "", "binlog.000004", 100),
				instance("mysql-1", "mysql-0", "binlog.000004", 90),
				instance("mysql-2", "mysql-0", "binlog.000004", 80),
			},
			host: "mysql-0",
			want: []string{"mysql-1", "mysql-2"},
		},
		"the primary has no source": {
			instances: []*orchestrator.Instance{
				instance("mysql-0", "", "binlog.000004", 100),
			},
			host: "mysql-0",
			want: []string{},
		},
		"instances of another source are left alone": {
			instances: []*orchestrator.Instance{
				instance("mysql-1", "mysql-0", "binlog.000004", 90),
				instance("mysql-2", "mysql-1", "binlog.000004", 80),
			},
			host: "mysql-1",
			want: []string{"mysql-2"},
		},
		"unknown source": {
			instances: []*orchestrator.Instance{
				instance("mysql-1", "mysql-0", "binlog.000004", 90),
			},
			host: "mysql-9",
			want: []string{},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			hosts := make([]string, 0)
			for _, replica := range replicasOf(tt.instances, tt.host) {
				hosts = append(hosts, replica.Key.Hostname)
			}

			assert.Equal(t, tt.want, hosts)
		})
	}
}

func TestMostUpToDate(t *testing.T) {
	tests := map[string]struct {
		replicas []*orchestrator.Instance
		want     string
	}{
		"the furthest into the binary log": {
			replicas: []*orchestrator.Instance{
				instance("mysql-1", "mysql-0", "binlog.000004", 100),
				instance("mysql-2", "mysql-0", "binlog.000004", 900),
			},
			want: "mysql-2",
		},
		"a later binary log beats a larger offset": {
			replicas: []*orchestrator.Instance{
				instance("mysql-1", "mysql-0", "binlog.000004", 900),
				instance("mysql-2", "mysql-0", "binlog.000005", 100),
			},
			want: "mysql-2",
		},
		"the tenth binary log is later than the ninth": {
			replicas: []*orchestrator.Instance{
				instance("mysql-1", "mysql-0", "binlog.000009", 100),
				instance("mysql-2", "mysql-0", "binlog.000010", 100),
			},
			want: "mysql-2",
		},
		"a replica with problems comes last even when it applied more": {
			replicas: []*orchestrator.Instance{
				instance("mysql-1", "mysql-0", "binlog.000005", 900, "not_replicating"),
				instance("mysql-2", "mysql-0", "binlog.000004", 100),
			},
			want: "mysql-2",
		},
		"equal positions are broken by hostname": {
			replicas: []*orchestrator.Instance{
				instance("mysql-2", "mysql-0", "binlog.000004", 100),
				instance("mysql-1", "mysql-0", "binlog.000004", 100),
			},
			want: "mysql-1",
		},
		"positions orchestrator did not report are broken by hostname": {
			replicas: []*orchestrator.Instance{
				instance("mysql-2", "mysql-0", "", 0),
				instance("mysql-1", "mysql-0", "", 0),
			},
			want: "mysql-1",
		},
		"a single replica": {
			replicas: []*orchestrator.Instance{
				instance("mysql-1", "mysql-0", "binlog.000004", 100, "not_replicating"),
			},
			want: "mysql-1",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, mostUpToDate(tt.replicas).Hostname)
		})
	}
}

func TestPodName(t *testing.T) {
	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster1", Namespace: "ps"},
	}

	tests := map[string]struct {
		host string
		want string
	}{
		"the hostname orchestrator reports": {
			host: "cluster1-mysql-0.cluster1-mysql.ps",
			want: "cluster1-mysql-0",
		},
		"without the namespace": {
			host: "cluster1-mysql-0.cluster1-mysql",
			want: "cluster1-mysql-0",
		},
		"a bare pod name": {
			host: "cluster1-mysql-0",
			want: "cluster1-mysql-0",
		},
		"a host of another cluster is left alone": {
			host: "cluster2-mysql-0.cluster2-mysql.ps2",
			want: "cluster2-mysql-0.cluster2-mysql.ps2",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, podName(cr, tt.host))
		})
	}
}

func TestRunFailoverSkipsNonFailoverRecoveries(t *testing.T) {
	tests := []string{
		"NoWriteableMasterStructureWarning",
		"MasterSingleReplicaNotReplicating",
		"UnreachableMaster",
		"",
	}

	for _, failureType := range tests {
		t.Run(failureType, func(t *testing.T) {
			err := runFailover(context.Background(), []string{
				"-source", "cluster1-mysql-0.cluster1-mysql.ps",
				"-failure-type", failureType,
			})

			require.NoError(t, err)
		})
	}
}
