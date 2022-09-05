package replicator

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql/client"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

func GetTopology(ctx context.Context, clusterHint string, user apiv1alpha1.SystemUser, pass string) (string, []string, error) {
	l := log.FromContext(ctx).WithName("MySQL Topology")

	primary, replicas, err := getTopologyFromOrchestrator(ctx, clusterHint)
	if err == nil {
		return primary, replicas, nil
	}

	l.Error(err, "failed to get topology from orchestrator")

	peers, err := getMySQLPeers(clusterHint)
	if err != nil {
		return "", nil, errors.Wrap(err, "get MySQL peers")
	}

	for _, peer := range peers.List() {
		host := peer

		db, err := client.New(user, pass, host, mysql.DefaultAdminPort, "performance_schema")
		if err != nil {
			return "", nil, errors.Wrapf(err, "connect to MySQL pod %s", host)
		}
		defer db.Close()

		l.V(1).Info("connected to MySQL pod", "host", host)

		status, primary, err := getReplicationStatus(db)
		if err != nil {
			return "", nil, errors.Wrapf(err, "get replication status of MySQL pod %s", host)
		}

		if status == ReplicationStatusError {
			l.Info("Replication is unhealhty in MySQL pod", "host", host)
			continue
		}

		if status == ReplicationStatusActive {
			host = primary
			l.V(1).Info("connecting to the primary", "host", host)

			db, err := client.New(user, pass, primary, mysql.DefaultAdminPort, "performance_schema")
			if err != nil {
				return "", nil, errors.Wrapf(err, "connect to MySQL pod %s", host)
			}
			defer db.Close()
		}

		replicas, err = showReplicas(db)
		if err != nil {
			return "", nil, errors.Wrapf(err, "get replicas connected to MySQL pod %s", host)
		}

		return host, replicas, nil
	}

	return "", nil, errors.New("failed to detect topology")
}

func getTopologyFromOrchestrator(ctx context.Context, clusterHint string) (string, []string, error) {
	c := strings.Split(clusterHint, ".")
	clusterName, namespace := c[0], c[1]

	apiHost := fmt.Sprintf("http://%s-%s-%d.%s:%d", clusterName, orchestrator.ComponentName, 0, namespace, orchestrator.DefaultWebPort)

	if err := orchestrator.RaftHealth(ctx, apiHost); err != nil {
		return "", nil, errors.Wrap(err, "check orchestrator node raft health")
	}

	primary, err := orchestrator.ClusterPrimary(ctx, apiHost, clusterHint)
	if err != nil {
		return "", nil, errors.Wrap(err, "get primary from orchestrator")
	}

	replicas := make([]string, len(primary.Replicas))
	for i, inst := range primary.Replicas {
		replicas[i] = inst.Hostname
	}

	return primary.Key.Hostname, replicas, nil
}

func getReplicationStatus(db *sql.DB) (ReplicationStatus, string, error) {
	row := db.QueryRow(`
    SELECT
	    connection_status.SERVICE_STATE,
	    applier_status.SERVICE_STATE,
      HOST
    FROM replication_connection_status connection_status
    JOIN replication_connection_configuration connection_configuration
      ON connection_status.channel_name = connection_configuration.channel_name
    JOIN replication_applier_status applier_status
      ON connection_status.channel_name = applier_status.channel_name
    WHERE connection_status.channel_name = ?
  `, DefaultChannelName)

	var ioState, sqlState, primary string
	if err := row.Scan(&ioState, &sqlState, &primary); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReplicationStatusNotInitiated, "", nil
		}
		return ReplicationStatusError, "", errors.Wrap(err, "scan replication status")
	}

	if ioState == "ON" && sqlState == "ON" {
		return ReplicationStatusActive, primary, nil
	}

	return ReplicationStatusNotInitiated, "", nil
}

func showReplicas(db *sql.DB) ([]string, error) {
	rows, err := db.Query("SHOW REPLICAS")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("not a primary")
		}
		return nil, errors.Wrap(err, "run SHOW REPLICAS")
	}
	defer rows.Close()

	type replica struct {
		ServerID    string
		Host        string
		Port        int
		SourceID    string
		ReplicaUUID string
	}
	replicas := make([]string, 0)

	for rows.Next() {
		var r replica
		if err := rows.Scan(&r.ServerID, &r.Host, &r.Port, &r.SourceID, &r.ReplicaUUID); err != nil {
			return nil, errors.Wrap(err, "scan row")
		}
		replicas = append(replicas, r.Host)
	}

	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, "scan rows")
	}

	return replicas, nil
}

func lookup(svcName string) (sets.String, error) {
	endpoints := sets.NewString()
	_, srvRecords, err := net.LookupSRV("", "", svcName)
	if err != nil {
		return endpoints, err
	}
	for _, srvRecord := range srvRecords {
		// The SRV records have the pattern $HOSTNAME.$SERVICE.$.NAMESPACE.svc.$CLUSTER_DNS_SUFFIX
		srv := strings.Split(srvRecord.Target, ".")
		ep := strings.Join(srv[:3], ".")
		endpoints.Insert(ep)
	}
	return endpoints, nil
}

func getMySQLPeers(clusterHint string) (sets.String, error) {
	c := strings.Split(clusterHint, ".")
	clusterName, namespace := c[0], c[1]

	svcName := fmt.Sprintf("%s-%s.%s", clusterName, mysql.ComponentName, namespace)

	peers, err := lookup(svcName)
	if err != nil {
		return nil, errors.Wrapf(err, "lookup %s", svcName)
	}

	return peers, nil
}
