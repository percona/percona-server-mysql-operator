package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func instance(host string, master string, logFile string, logPos int64, problems ...string) *Instance {
	return &Instance{
		Key:                   InstanceKey{Hostname: host, Port: 3306},
		MasterKey:             InstanceKey{Hostname: master, Port: 3306},
		Problems:              problems,
		ExecBinlogCoordinates: BinlogCoordinates{LogFile: logFile, LogPos: logPos},
	}
}

func TestReplicasOf(t *testing.T) {
	tests := map[string]struct {
		instances []*Instance
		host      string
		want      []string
	}{
		"replicas of the primary": {
			instances: []*Instance{
				instance("mysql-0", "", "binlog.000004", 100),
				instance("mysql-1", "mysql-0", "binlog.000004", 90),
				instance("mysql-2", "mysql-0", "binlog.000004", 80),
			},
			host: "mysql-0",
			want: []string{"mysql-1", "mysql-2"},
		},
		"the primary has no source": {
			instances: []*Instance{
				instance("mysql-0", "", "binlog.000004", 100),
			},
			host: "mysql-0",
			want: []string{},
		},
		"instances of another source are left alone": {
			instances: []*Instance{
				instance("mysql-1", "mysql-0", "binlog.000004", 90),
				instance("mysql-2", "mysql-1", "binlog.000004", 80),
			},
			host: "mysql-1",
			want: []string{"mysql-2"},
		},
		"unknown source": {
			instances: []*Instance{
				instance("mysql-1", "mysql-0", "binlog.000004", 90),
			},
			host: "mysql-9",
			want: []string{},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			hosts := make([]string, 0)
			for _, replica := range ReplicasOf(tt.instances, tt.host) {
				hosts = append(hosts, replica.Key.Hostname)
			}

			assert.Equal(t, tt.want, hosts)
		})
	}
}

func TestMostUpToDate(t *testing.T) {
	tests := map[string]struct {
		replicas []*Instance
		want     string
	}{
		"the furthest into the binary log": {
			replicas: []*Instance{
				instance("mysql-1", "mysql-0", "binlog.000004", 100),
				instance("mysql-2", "mysql-0", "binlog.000004", 900),
			},
			want: "mysql-2",
		},
		"a later binary log beats a larger offset": {
			replicas: []*Instance{
				instance("mysql-1", "mysql-0", "binlog.000004", 900),
				instance("mysql-2", "mysql-0", "binlog.000005", 100),
			},
			want: "mysql-2",
		},
		"the tenth binary log is later than the ninth": {
			replicas: []*Instance{
				instance("mysql-1", "mysql-0", "binlog.000009", 100),
				instance("mysql-2", "mysql-0", "binlog.000010", 100),
			},
			want: "mysql-2",
		},
		"a replica with problems comes last even when it applied more": {
			replicas: []*Instance{
				instance("mysql-1", "mysql-0", "binlog.000005", 900, "not_replicating"),
				instance("mysql-2", "mysql-0", "binlog.000004", 100),
			},
			want: "mysql-2",
		},
		"equal positions are broken by hostname": {
			replicas: []*Instance{
				instance("mysql-2", "mysql-0", "binlog.000004", 100),
				instance("mysql-1", "mysql-0", "binlog.000004", 100),
			},
			want: "mysql-1",
		},
		"positions orchestrator did not report are broken by hostname": {
			replicas: []*Instance{
				instance("mysql-2", "mysql-0", "", 0),
				instance("mysql-1", "mysql-0", "", 0),
			},
			want: "mysql-1",
		},
		"a single replica": {
			replicas: []*Instance{
				instance("mysql-1", "mysql-0", "binlog.000004", 100, "not_replicating"),
			},
			want: "mysql-1",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, MostUpToDate(tt.replicas).Hostname)
		})
	}
}
