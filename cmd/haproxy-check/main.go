package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql/topology"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	args := os.Args[1:]
	if len(args) < 3 {
		log.Fatalln("Too few arguments")
	}
	host := args[2]

	fqdn, err := getHostFQDN(host)
	if err != nil {
		log.Fatalln("Failed to get MySQL node FQDN: ", err.Error())
	}

	operatorPass, err := getSecret(string(apiv1alpha1.UserOperator))
	if err != nil {
		log.Fatalln("Failed to get secret:", err.Error())
	}
	t, err := topology.GetAsync(ctx, operatorPass, fqdn)
	if err != nil {
		log.Fatalln("Failed to get topology:", err.Error())
	}
	ioRunning, sqlRunning, err := replicationStatus(ctx, fqdn, operatorPass)
	if err != nil {
		log.Fatalln("Failed to get replication status:", err.Error())
	}
	readOnly, err := readOnly(ctx, fqdn, operatorPass)
	if err != nil {
		log.Fatalf("Failed to check if host is readonly: %s\n", host)
	}

	log.Printf("MySQL node %s:%d\n", fqdn, mysql.DefaultAdminPort)
	log.Printf("read_only: %t\n", readOnly)
	log.Printf("Replica_IO_Running: %t\n", ioRunning)
	log.Printf("Replica_SQL_Running: %t\n", sqlRunning)

	proxyName := os.Getenv("HAPROXY_PROXY_NAME")
	if (t.IsPrimary(fqdn) && !readOnly && !ioRunning && !sqlRunning) ||
		(t.HasReplica(fqdn) && readOnly && ioRunning && sqlRunning) {
		log.Printf("MySQL node %s:%d for backend %s is ok\n", fqdn, mysql.DefaultAdminPort, proxyName)
	} else {
		log.Printf("MySQL node %s:%d for backend %s is not ok\n", fqdn, mysql.DefaultAdminPort, proxyName)
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

func readOnly(ctx context.Context, host, rootPass string) (bool, error) {
	db, err := replicator.NewReplicator(apiv1alpha1.UserOperator,
		rootPass,
		host,
		mysql.DefaultAdminPort)
	if err != nil {
		return false, errors.Wrapf(err, "connect to %v", host)
	}
	defer db.Close()
	return db.IsReadonly()
}

func replicationStatus(ctx context.Context, host, rootPass string) (bool, bool, error) {
	db, err := replicator.NewReplicator(apiv1alpha1.UserOperator,
		rootPass,
		host,
		mysql.DefaultAdminPort)
	if err != nil {
		return false, false, errors.Wrapf(err, "connect to %v", host)
	}
	defer db.Close()

	status, err := db.ShowReplicaStatus(ctx)
	if err != nil {
		return false, false, errors.Wrap(err, "get replica status")
	}
	ioRunning := status["Replica_IO_Running"]
	sqlRunning := status["Replica_SQL_Running"]
	return ioRunning == "Yes", sqlRunning == "Yes", nil
}

func getSecret(username string) (string, error) {
	path := filepath.Join(mysql.CredsMountPath, username)
	sBytes, err := os.ReadFile(path)
	if err != nil {
		return "", errors.Wrapf(err, "read %s", path)
	}

	return strings.TrimSpace(string(sBytes)), nil
}
