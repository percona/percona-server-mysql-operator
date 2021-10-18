package orchestrator

import (
	"encoding/json"

	"github.com/openark/orchestrator/go/config"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (o *Orchestrator) Configuration() *config.Configuration {
	return &config.Configuration{
		ApplyMySQLPromotionAfterMasterFailover:    true,
		BackendDB:                                 "sqlite",
		Debug:                                     true,
		DetachLostReplicasAfterMasterFailover:     true,
		DetectClusterAliasQuery:                   "SELECT CONCAT(SUBSTRING(@@hostname, 1, LENGTH(@@hostname) - 1 - LENGTH(SUBSTRING_INDEX(@@hostname,'-',-2))),'.',SUBSTRING_INDEX(@@report_host,'.',-1))",
		DetectInstanceAliasQuery:                  "SELECT @@hostname",
		DiscoverByShowSlaveHosts:                  true,
		FailMasterPromotionIfSQLThreadNotUpToDate: true,
		HostnameResolveMethod:                     "none",
		InstancePollSeconds:                       5,
		ListenAddress:                             ":3000",
		MasterFailoverDetachReplicaMasterHost:     true,
		MySQLHostnameResolveMethod:                "@@report_host",
		MySQLTopologyCredentialsConfigFile:        CredsMountPath + "/orc-topology.cnf",
		OnFailureDetectionProcesses: []string{
			"/usr/local/bin/orc-helper event -w '{failureClusterAlias}' 'OrcFailureDetection' 'Failure: {failureType}, failed host: {failedHost}, lost replcas: {lostReplicas}' || true",
			"/usr/local/bin/orc-helper failover-in-progress '{failureClusterAlias}' '{failureDescription}' || true",
		},
		PostIntermediateMasterFailoverProcesses: []string{
			"/usr/local/bin/orc-helper event '{failureClusterAlias}' 'OrcPostIntermediateMasterFailover' 'Failure type: {failureType}, failed hosts: {failedHost}, slaves: {countSlaves}' || true",
		},
		PostMasterFailoverProcesses: []string{
			"/usr/local/bin/orc-helper event '{failureClusterAlias}' 'OrcPostMasterFailover' 'Failure type: {failureType}, new master: {successorHost}, slaves: {slaveHosts}' || true",
		},
		PostUnsuccessfulFailoverProcesses: []string{
			"/usr/local/bin/orc-helper event -w '{failureClusterAlias}' 'OrcPostUnsuccessfulFailover' 'Failure: {failureType}, failed host: {failedHost} with {countSlaves} slaves' || true",
		},
		PreFailoverProcesses: []string{
			"/usr/local/bin/orc-helper failover-in-progress '{failureClusterAlias}' '{failureDescription}' || true",
		},
		ProcessesShellCommand:                   "sh",
		DefaultRaftPort:                         10008,
		RaftBind:                                "127.0.0.1",
		RaftDataDir:                             DataMountPath,
		RaftEnabled:                             true,
		RaftNodes:                               []string{"127.0.0.1"},
		RecoverIntermediateMasterClusterFilters: []string{".*"},
		RecoverMasterClusterFilters:             []string{".*"},
		RecoveryIgnoreHostnameFilters:           []string{},
		RecoveryPeriodBlockSeconds:              300,
		RemoveTextFromHostnameDisplay:           ":3306",
		SQLite3DataFile:                         DataMountPath + "/orc.db",
		SlaveLagQuery:                           "SELECT TIMESTAMPDIFF(SECOND,ts,NOW()) as drift FROM sys_operator.heartbeat ORDER BY drift ASC LIMIT 1",
		StatusEndpoint:                          "/api/status",
		UnseenInstanceForgetHours:               1,
	}
}

func (o *Orchestrator) ConfigMap(c *config.Configuration) (*corev1.ConfigMap, error) {
	cBytes, err := json.Marshal(c)
	if err != nil {
		return nil, errors.Wrap(err, "marshal config to json")
	}

	data := map[string]string{
		"orchestrator.conf.json": string(cBytes),
	}

	return k8s.ConfigMap(o.Namespace(), o.Name(), data), nil
}

func (o *Orchestrator) Secret(username, password string) *corev1.Secret {
	credentials := "[client]"
	credentials += "\nuser = " + username
	credentials += "\npassword = " + password

	data := map[string]string{
		"orc-topology.cnf": credentials,
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      o.Name(),
			Namespace: o.Namespace(),
		},
		StringData: data,
	}
}
