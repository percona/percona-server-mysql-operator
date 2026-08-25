package clusterset

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestMySQLTimediffToDuration(t *testing.T) {
	testCases := []struct {
		desc     string
		timediff string
		expected *time.Duration
	}{
		{
			desc:     "seconds only",
			timediff: "00:00:05",
			expected: new(5 * time.Second),
		},
		{
			desc:     "hours, minutes and seconds",
			timediff: "01:02:03",
			expected: new(time.Hour + 2*time.Minute + 3*time.Second),
		},
		{
			desc:     "microsecond precision",
			timediff: "00:00:01.500000",
			expected: new(1500 * time.Millisecond),
		},
		{
			desc:     "no lag",
			timediff: "00:00:00",
			expected: new(time.Duration(0)),
		},
		{
			desc:     "replica ahead of source reports a negative timediff",
			timediff: "-00:00:01",
			expected: new(-time.Second),
		},
		{
			desc:     "empty string",
			timediff: "",
			expected: nil,
		},
		{
			desc:     "not enough parts",
			timediff: "00:05",
			expected: nil,
		},
		{
			desc:     "too many parts",
			timediff: "00:00:00:05",
			expected: nil,
		},
		{
			desc:     "unparseable parts",
			timediff: "aa:bb:cc",
			expected: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			assert.Equal(t, tc.expected, mysqlTimediffToDuration(tc.timediff))
		})
	}
}

func TestClusterStatusGetPrimaryMemberReplicationLagSeconds(t *testing.T) {
	testCases := []struct {
		desc     string
		cluster  ClusterStatus
		expected *int64
	}{
		{
			desc: "primary cluster is the source and never reports lag",
			cluster: ClusterStatus{
				ClusterRole: ClusterRolePrimary,
				Topology: map[string]TopologyStatus{
					"dc1-mysql-0:3306": {
						MemberRole:                       ClusterRolePrimary,
						MemberState:                      "ONLINE",
						ReplicationLagFromOriginalSource: "00:00:07",
					},
				},
			},
			expected: nil,
		},
		{
			desc: "replica cluster reports the lag of its primary member",
			cluster: ClusterStatus{
				ClusterRole: ClusterRoleReplica,
				Topology: map[string]TopologyStatus{
					"dc2-mysql-0:3306": {
						MemberRole:                       ClusterRolePrimary,
						MemberState:                      "ONLINE",
						ReplicationLagFromOriginalSource: "00:00:05",
					},
				},
			},
			expected: new(int64(5)),
		},
		{
			desc: "secondary members are ignored",
			cluster: ClusterStatus{
				ClusterRole: ClusterRoleReplica,
				Topology: map[string]TopologyStatus{
					"dc2-mysql-0:3306": {
						MemberRole:                       ClusterRolePrimary,
						MemberState:                      "ONLINE",
						ReplicationLagFromOriginalSource: "00:00:02",
					},
					"dc2-mysql-1:3306": {
						MemberRole:                       "SECONDARY",
						MemberState:                      "ONLINE",
						ReplicationLagFromOriginalSource: "00:10:00",
					},
				},
			},
			expected: new(int64(2)),
		},
		{
			desc: "lag from the immediate source is not used",
			cluster: ClusterStatus{
				ClusterRole: ClusterRoleReplica,
				Topology: map[string]TopologyStatus{
					"dc3-mysql-0:3306": {
						MemberRole:                        ClusterRolePrimary,
						MemberState:                       "ONLINE",
						ReplicationLagFromImmediateSource: "00:00:01",
						ReplicationLagFromOriginalSource:  "00:00:20",
					},
				},
			},
			expected: new(int64(20)),
		},
		{
			desc: "hours and minutes are included in the reported lag",
			cluster: ClusterStatus{
				ClusterRole: ClusterRoleReplica,
				Topology: map[string]TopologyStatus{
					"dc2-mysql-0:3306": {
						MemberRole:                       ClusterRolePrimary,
						ReplicationLagFromOriginalSource: "01:02:03",
					},
				},
			},
			expected: new(int64(3723)),
		},
		{
			desc: "sub-second precision is truncated",
			cluster: ClusterStatus{
				ClusterRole: ClusterRoleReplica,
				Topology: map[string]TopologyStatus{
					"dc2-mysql-0:3306": {
						MemberRole:                       ClusterRolePrimary,
						ReplicationLagFromOriginalSource: "00:00:09.900000",
					},
				},
			},
			expected: new(int64(9)),
		},
		{
			desc: "sub-second lag is reported as zero",
			cluster: ClusterStatus{
				ClusterRole: ClusterRoleReplica,
				Topology: map[string]TopologyStatus{
					"dc2-mysql-0:3306": {
						MemberRole:                       ClusterRolePrimary,
						ReplicationLagFromOriginalSource: "00:00:00.400000",
					},
				},
			},
			expected: new(int64(0)),
		},
		{
			desc: "replica ahead of source reports negative lag",
			cluster: ClusterStatus{
				ClusterRole: ClusterRoleReplica,
				Topology: map[string]TopologyStatus{
					"dc2-mysql-0:3306": {
						MemberRole:                       ClusterRolePrimary,
						ReplicationLagFromOriginalSource: "-00:00:04",
					},
				},
			},
			expected: new(int64(-4)),
		},
		{
			desc: "lag is not reported yet",
			cluster: ClusterStatus{
				ClusterRole: ClusterRoleReplica,
				Topology: map[string]TopologyStatus{
					"dc2-mysql-0:3306": {
						MemberRole:                       ClusterRolePrimary,
						MemberState:                      "RECOVERING",
						ReplicationLagFromOriginalSource: "",
					},
				},
			},
			expected: nil,
		},
		{
			desc: "no primary member in topology",
			cluster: ClusterStatus{
				ClusterRole: ClusterRoleReplica,
				Topology: map[string]TopologyStatus{
					"dc2-mysql-0:3306": {
						MemberRole:                       "SECONDARY",
						ReplicationLagFromOriginalSource: "00:00:05",
					},
				},
			},
			expected: nil,
		},
		{
			desc: "empty topology",
			cluster: ClusterStatus{
				ClusterRole: ClusterRoleReplica,
				Topology:    map[string]TopologyStatus{},
			},
			expected: nil,
		},
		{
			desc: "topology is missing, e.g. status was not queried with extended output",
			cluster: ClusterStatus{
				ClusterRole: ClusterRoleReplica,
			},
			expected: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.cluster.getPrimaryMemberReplicationLagSeconds())
		})
	}
}

func TestClusterStatusesIntoAPI(t *testing.T) {
	testCases := []struct {
		desc     string
		clusters ClusterStatuses
		expected apiv1.ClusterSetClusterStatuses
	}{
		{
			desc:     "no clusters",
			clusters: nil,
			expected: apiv1.ClusterSetClusterStatuses{},
		},
		{
			desc: "primary and replica cluster",
			clusters: ClusterStatuses{
				"dc1": {
					ClusterRole:  ClusterRolePrimary,
					GlobalStatus: StatusHealthy,
					Primary:      "dc1-mysql-0:3306",
					Topology: map[string]TopologyStatus{
						"dc1-mysql-0:3306": {
							MemberRole:  ClusterRolePrimary,
							MemberState: "ONLINE",
						},
					},
				},
				"dc2": {
					ClusterRole:  ClusterRoleReplica,
					GlobalStatus: "OK",
					Primary:      "dc2-mysql-0:3306",
					Topology: map[string]TopologyStatus{
						"dc2-mysql-0:3306": {
							MemberRole:                       ClusterRolePrimary,
							MemberState:                      "ONLINE",
							ReplicationLagFromOriginalSource: "00:00:11",
						},
					},
				},
			},
			expected: apiv1.ClusterSetClusterStatuses{
				"dc1": {
					ClusterRole:  ClusterRolePrimary,
					GlobalStatus: StatusHealthy,
					Primary:      "dc1-mysql-0:3306",
				},
				"dc2": {
					ClusterRole:           ClusterRoleReplica,
					GlobalStatus:          "OK",
					Primary:               "dc2-mysql-0:3306",
					ReplicationLagSeconds: new(int64(11)),
				},
			},
		},
		{
			desc: "replica cluster without topology",
			clusters: ClusterStatuses{
				"dc2": {
					ClusterRole:  ClusterRoleReplica,
					GlobalStatus: StatusUnknown,
					Primary:      "dc2-mysql-0:3306",
				},
			},
			expected: apiv1.ClusterSetClusterStatuses{
				"dc2": {
					ClusterRole:  ClusterRoleReplica,
					GlobalStatus: StatusUnknown,
					Primary:      "dc2-mysql-0:3306",
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			assert.Equal(t, tc.expected, tc.clusters.IntoAPI())
		})
	}
}

func TestStatusUnmarshalsMysqlshOutput(t *testing.T) {
	testCases := []struct {
		desc     string
		output   string
		expected Status
	}{
		{
			desc: "extended output exposes per-member replication lag",
			output: `{
				"clusters": {
					"dc1": {
						"clusterRole": "PRIMARY",
						"globalStatus": "OK",
						"primary": "dc1-mysql-0.dc1-mysql.ns:3306",
						"status": "OK",
						"statusText": "Cluster is ONLINE and can tolerate up to ONE failure.",
						"topology": {
							"dc1-mysql-0.dc1-mysql.ns:3306": {
								"address": "dc1-mysql-0.dc1-mysql.ns:3306",
								"memberRole": "PRIMARY",
								"memberState": "ONLINE",
								"mode": "R/W",
								"status": "ONLINE",
								"version": "8.4.3"
							},
							"dc1-mysql-1.dc1-mysql.ns:3306": {
								"address": "dc1-mysql-1.dc1-mysql.ns:3306",
								"memberRole": "SECONDARY",
								"memberState": "ONLINE",
								"mode": "R/O",
								"replicationLagFromImmediateSource": "",
								"replicationLagFromOriginalSource": "",
								"status": "ONLINE",
								"version": "8.4.3"
							}
						}
					},
					"dc2": {
						"clusterRole": "REPLICA",
						"clusterSetReplication": {
							"applierStatus": "APPLYING",
							"receiverStatus": "ON",
							"source": "dc1-mysql-0.dc1-mysql.ns:3306"
						},
						"clusterSetReplicationStatus": "OK",
						"globalStatus": "OK",
						"primary": "dc2-mysql-0.dc2-mysql.ns:3306",
						"status": "OK_NO_TOLERANCE",
						"topology": {
							"dc2-mysql-0.dc2-mysql.ns:3306": {
								"address": "dc2-mysql-0.dc2-mysql.ns:3306",
								"memberRole": "PRIMARY",
								"memberState": "ONLINE",
								"mode": "R/O",
								"replicationLagFromImmediateSource": "00:00:03.120000",
								"replicationLagFromOriginalSource": "00:00:12.500000",
								"status": "ONLINE",
								"version": "8.4.3"
							}
						}
					}
				},
				"domainName": "ps-clusterset",
				"globalPrimaryInstance": "dc1-mysql-0.dc1-mysql.ns:3306",
				"primaryCluster": "dc1",
				"status": "HEALTHY",
				"statusText": "All Clusters available."
			}`,
			expected: Status{
				Clusters: ClusterStatuses{
					"dc1": {
						ClusterRole:  ClusterRolePrimary,
						GlobalStatus: "OK",
						Primary:      "dc1-mysql-0.dc1-mysql.ns:3306",
						Topology: map[string]TopologyStatus{
							"dc1-mysql-0.dc1-mysql.ns:3306": {
								Status:      "ONLINE",
								MemberRole:  ClusterRolePrimary,
								MemberState: "ONLINE",
							},
							"dc1-mysql-1.dc1-mysql.ns:3306": {
								Status:      "ONLINE",
								MemberRole:  "SECONDARY",
								MemberState: "ONLINE",
							},
						},
					},
					"dc2": {
						ClusterRole:  ClusterRoleReplica,
						GlobalStatus: "OK",
						Primary:      "dc2-mysql-0.dc2-mysql.ns:3306",
						Topology: map[string]TopologyStatus{
							"dc2-mysql-0.dc2-mysql.ns:3306": {
								Status:                            "ONLINE",
								MemberRole:                        ClusterRolePrimary,
								MemberState:                       "ONLINE",
								ReplicationLagFromImmediateSource: "00:00:03.120000",
								ReplicationLagFromOriginalSource:  "00:00:12.500000",
							},
						},
					},
				},
				DomainName:            "ps-clusterset",
				GlobalPrimaryInstance: "dc1-mysql-0.dc1-mysql.ns:3306",
				PrimaryCluster:        "dc1",
				Status:                StatusHealthy,
				StatusText:            "All Clusters available.",
			},
		},
		{
			desc: "output without topology",
			output: `{
				"clusters": {
					"dc1": {
						"clusterRole": "PRIMARY",
						"globalStatus": "OK",
						"primary": "dc1-mysql-0.dc1-mysql.ns:3306"
					}
				},
				"domainName": "ps-clusterset",
				"globalPrimaryInstance": "dc1-mysql-0.dc1-mysql.ns:3306",
				"primaryCluster": "dc1",
				"status": "HEALTHY",
				"statusText": "All Clusters available."
			}`,
			expected: Status{
				Clusters: ClusterStatuses{
					"dc1": {
						ClusterRole:  ClusterRolePrimary,
						GlobalStatus: "OK",
						Primary:      "dc1-mysql-0.dc1-mysql.ns:3306",
					},
				},
				DomainName:            "ps-clusterset",
				GlobalPrimaryInstance: "dc1-mysql-0.dc1-mysql.ns:3306",
				PrimaryCluster:        "dc1",
				Status:                StatusHealthy,
				StatusText:            "All Clusters available.",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			status := Status{}
			require.NoError(t, json.Unmarshal([]byte(tc.output), &status))
			assert.Equal(t, tc.expected, status)
		})
	}
}
