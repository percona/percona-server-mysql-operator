package manager

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clusterset"
	"github.com/percona/percona-server-mysql-operator/pkg/mysqlsh"
)

// sensitiveRegexp scrubs passwords from connection errors before they surface.
var sensitiveRegexp = regexp.MustCompile(":.*@")

const clusterSetAsyncChannel = "clusterset_replication"

// sqlConnector opens go-sql-driver connections directly from the operator to a
// cluster's endpoints.
type sqlConnector struct{}

func dsn(user, pass, host string, port int32) string {
	config := mysql.NewConfig()
	config.User = user
	config.Passwd = pass
	config.Net = "tcp"
	config.Addr = fmt.Sprintf("%s:%d", host, port)
	config.DBName = "performance_schema"
	config.Params = map[string]string{
		"interpolateParams": "true",
		"timeout":           "10s",
		"readTimeout":       "30s",
		"writeTimeout":      "30s",
		"tls":               "preferred",
	}
	return config.FormatDSN()
}

func (sqlConnector) connect(ctx context.Context, endpoints []apiv1.ClusterSetClusterEndpoint, pass string) (clusterConn, error) {
	var lastErr error
	for _, ep := range endpoints {
		db, err := sql.Open("mysql", dsn(string(apiv1.UserClusterSet), pass, ep.Host, ep.GetPort()))
		if err != nil {
			lastErr = err
			continue
		}
		if err := db.PingContext(ctx); err != nil {
			db.Close()
			lastErr = err
			continue
		}
		return &sqlConn{db: db}, nil
	}

	if lastErr == nil {
		return nil, errors.Wrap(mysqlsh.ErrEndpointUnreachable, "no endpoints")
	}
	return nil, errors.Wrapf(mysqlsh.ErrEndpointUnreachable, "%s", sensitiveRegexp.ReplaceAllString(lastErr.Error(), ":*****@"))
}

// sqlConn implements clusterConn over a single go-sql-driver connection.
type sqlConn struct {
	db *sql.DB
}

func (c *sqlConn) Close() error { return c.db.Close() }

func (c *sqlConn) DomainName(ctx context.Context) (string, error) {
	var name string
	err := c.db.QueryRowContext(ctx, "SELECT domain_name FROM mysql_innodb_cluster_metadata.clustersets LIMIT 1").Scan(&name)
	return name, errors.Wrap(err, "query clusterset domain name")
}

func (c *sqlConn) Members(ctx context.Context) ([]memberMeta, error) {
	rows, err := c.db.QueryContext(ctx, "SELECT cluster_name, member_role FROM mysql_innodb_cluster_metadata.v2_cs_members")
	if err != nil {
		return nil, errors.Wrap(err, "query clusterset members")
	}
	defer rows.Close()

	members := make([]memberMeta, 0)
	for rows.Next() {
		var name string
		var role sql.NullString
		if err := rows.Scan(&name, &role); err != nil {
			return nil, errors.Wrap(err, "scan clusterset member")
		}
		// member_role is 'PRIMARY', 'REPLICA' or NULL (invalidated); anything
		// other than PRIMARY is treated as a replica.
		clusterRole := clusterset.ClusterRoleReplica
		if role.Valid && role.String == clusterset.ClusterRolePrimary {
			clusterRole = clusterset.ClusterRolePrimary
		}
		members = append(members, memberMeta{Name: name, Role: clusterRole})
	}
	return members, errors.Wrap(rows.Err(), "iterate clusterset members")
}

func (c *sqlConn) GRPrimary(ctx context.Context) (string, error) {
	var host string
	var port int32
	err := c.db.QueryRowContext(ctx, "SELECT MEMBER_HOST, MEMBER_PORT FROM performance_schema.replication_group_members WHERE MEMBER_ROLE='PRIMARY' AND MEMBER_STATE='ONLINE'").Scan(&host, &port)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", errors.Wrap(err, "query group replication primary")
	}
	return fmt.Sprintf("%s:%d", host, port), nil
}

func (c *sqlConn) InPrimaryPartition(ctx context.Context) (bool, error) {
	var in bool
	err := c.db.QueryRowContext(ctx, `
		SELECT
			MEMBER_STATE = 'ONLINE'
			AND (
				(
					SELECT COUNT(*)
					FROM performance_schema.replication_group_members
					WHERE MEMBER_STATE NOT IN ('ONLINE', 'RECOVERING')
				) >= (
					(SELECT COUNT(*) FROM performance_schema.replication_group_members) / 2
				) = 0
			)
		FROM performance_schema.replication_group_members
			JOIN performance_schema.replication_group_member_stats USING(member_id)
		WHERE member_id = @@global.server_uuid`).Scan(&in)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return in, errors.Wrap(err, "check primary partition")
}

func (c *sqlConn) ClusterSetReplicationRunning(ctx context.Context) (bool, error) {
	var running bool
	err := c.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM performance_schema.replication_connection_status conn
			JOIN performance_schema.replication_applier_status appl USING (CHANNEL_NAME)
			WHERE conn.CHANNEL_NAME = ?
				AND conn.SERVICE_STATE = 'ON'
				AND appl.SERVICE_STATE = 'ON'
		)`, clusterSetAsyncChannel).Scan(&running)
	return running, errors.Wrap(err, "check clusterset replication running")
}
