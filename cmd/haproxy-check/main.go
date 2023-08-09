package main

import (
	"context"
	"fmt"
	//"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql/topology"
)

const (
	backendNamePrimary = "mysql-primary"
	backendNameReplica = "mysql-replicas"
)

var log = logf.Log.WithName("haproxy-check")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	opts := zap.Options{
		Development: true,
		DestWriter:  os.Stdout,
	}
	logf.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	args := os.Args[1:]
	if len(args) < 3 {
		log.Error(errors.New("too few arguments"), "failed to validate arguments")
		os.Exit(1)
	}
	host := args[2]

	backendName, ok := os.LookupEnv("HAPROXY_PROXY_NAME")
	if !ok {
		log.Error(errors.New("backend name is not set"), "failed to get backend name from `HAPROXY_PROXY_NAME`")
		os.Exit(1)
	}
	if backendName != backendNamePrimary && backendName != backendNameReplica {
		log.Error(errors.Errorf("backend name is unknown: %s", backendName), "failed to get backend name from `HAPROXY_PROXY_NAME`")
		os.Exit(1)
	}

	fqdn, err := getHostFQDN(host)
	if err != nil {
		log.Error(err, "failed to get MySQL node FQDN")
		os.Exit(1)
	}

	operatorPass, err := getSecret(string(apiv1alpha1.UserOperator))
	if err != nil {
		log.Error(err, "failed to get operator password")
		os.Exit(1)
	}

	t, err := getTopology(ctx, operatorPass, fqdn, host)
	if err != nil {
		log.Error(err, "failed to get topology")
		os.Exit(1)
	}

	if (t.IsPrimary(fqdn) && backendName == backendNamePrimary) || (t.HasReplica(fqdn) && backendName == backendNameReplica) {
		log.Info(fmt.Sprintf("MySQL node %s:%d for backend %s is ok", fqdn, mysql.DefaultAdminPort, backendName))
	} else {
		log.Info(fmt.Sprintf("MySQL node %s:%d for backend %s is not ok", fqdn, mysql.DefaultAdminPort, backendName))
		os.Exit(1)
	}
}

func getHostFQDN(addr string) (string, error) {
	names, err := net.LookupAddr(addr)
	if err != nil {
		return "", errors.Wrap(err, "failed to retrieve hostname")
	}
	if len(names) == 0 {
		return "", errors.New("hostname array is empty")
	}
	// names[0] contains value in this format: cluster1-mysql-0.cluster1-mysql.some-namespace.svc.cluster.local.
	// but we need it to be like this: cluster1-mysql-0.cluster1-mysql.some-namespace
	spl := strings.Split(names[0], ".")

	fqdn := strings.Join(spl[:3], ".")
	return fqdn, nil
}

func getSecret(username string) (string, error) {
	path := filepath.Join(mysql.CredsMountPath, username)
	sBytes, err := os.ReadFile(path)
	if err != nil {
		return "", errors.Wrapf(err, "read %s", path)
	}

	return strings.TrimSpace(string(sBytes)), nil
}

func getTopology(ctx context.Context, operatorPass, fqdn, host string) (topology.Topology, error) {
	ns, err := k8s.DefaultAPINamespace()
	if err != nil {
		return topology.Topology{}, errors.Wrap(err, "failed to get namespace")
	}

	clusterTypeData, err := getFileValue("/tmp/cluster_type")
	if err != nil {
		return topology.Topology{}, errors.Wrap(err, "failed to read cluster type")
	}
	clusterType := apiv1alpha1.ClusterType(clusterTypeData)
	if !clusterType.IsValid() {
		return topology.Topology{}, errors.Errorf("unknown cluster type: %s", string(clusterType))
	}

	crName, err := getFileValue("/tmp/cluster_name")
	if err != nil {
		return topology.Topology{}, errors.Wrap(err, "failed to get cluster name")
	}
	expTopologyOpt, err := getFileValue("/tmp/experimental_topology")
	if err != nil {
		return topology.Topology{}, errors.Wrap(err, "failed to experimental topology option")
	}

	tm, err := topology.NewTopologyManager(clusterType, &apiv1alpha1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      crName,
			Namespace: ns,
		},
	}, operatorPass, fqdn).DisableOrchestrator(expTopologyOpt == "true").Manager()
	if err != nil {
		return topology.Topology{}, errors.Wrap(err, "failed to create topology manager")
	}

	db, err := tm.Replicator(ctx, fqdn)
	if err != nil {
		return topology.Topology{}, errors.Wrap(err, "failed to get replicator")
	}
	defer db.Close()

	readOnly, err := db.IsReadonly(ctx)
	if err != nil {
		return topology.Topology{}, errors.Wrapf(err, "failed to check if host is readonly: %s", host)
	}
	switch clusterType {
	case apiv1alpha1.ClusterTypeAsync:
		status, err := db.ShowReplicaStatus(ctx)
		if err != nil {
			return topology.Topology{}, errors.Wrap(err, "get replica status")
		}
		log.Info(fmt.Sprintf("%s:%d super_read_only: %t Replica_IO_Running: %s Replica_SQL_Running: %s", host, mysql.DefaultAdminPort, readOnly, status["Replica_IO_Running"], status["Replica_SQL_Running"]))
	case apiv1alpha1.ClusterTypeGR:
		applier, err := db.GetGroupReplicationApplierStatus(ctx)
		if err != nil {
			return topology.Topology{}, errors.Wrap(err, "failed to get GR applier status")
		}
		log.Info(fmt.Sprintf("%s:%d super_read_only: %t Applier: %s", host, mysql.DefaultAdminPort, readOnly, applier))
	}

	return tm.Get(ctx)
}

func getFileValue(filename string) (string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", errors.Wrapf(err, "failed to read file %s", filename)
	}
	return strings.TrimSpace(string(data)), nil
}
