package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	database "github.com/percona/percona-server-mysql-operator/cmd/internal/db"
	state "github.com/percona/percona-server-mysql-operator/cmd/internal/naming"
	mysqldb "github.com/percona/percona-server-mysql-operator/pkg/db"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

const (
	noBootstrapFile      = "/var/lib/mysql/no-bootstrap"
	manualRecoveryFile   = "/var/lib/mysql/sleep-forever"
	fullClusterCrashFile = "/var/lib/mysql/full-cluster-crash"
)

func main() {
	recoveryFiles := []string{
		noBootstrapFile,
		manualRecoveryFile,
		fullClusterCrashFile,
	}
	for _, rFile := range recoveryFiles {
		recovery, err := fileExists(rFile)
		if err == nil && recovery {
			log.Printf("%s exists. exiting...", rFile)
			os.Exit(0)
		}
	}

	stateFilePath, ok := os.LookupEnv(naming.EnvMySQLStateFile)
	if !ok {
		log.Fatalln("MYSQL_STATE_FILE env variable is required")
	}
	mysqlState, err := os.ReadFile(stateFilePath)
	if err != nil {
		log.Fatalf("read mysql state: %s", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch os.Args[1] {
	case "readiness":
		if string(mysqlState) != string(state.MySQLReady) {
			log.Println("MySQL state is not ready...")
			os.Exit(1)
		}
		switch os.Getenv("CLUSTER_TYPE") {
		case "async":
			if err := checkReadinessAsync(ctx); err != nil {
				log.Fatalf("readiness check failed: %v", err)
			}
		case "group-replication":
			if err := checkReadinessGR(ctx); err != nil {
				log.Fatalf("readiness check failed: %v", err)
			}
		}
	case "liveness":
		if string(mysqlState) == string(state.MySQLStartup) {
			log.Println("MySQL is starting up, not killing it...")
			os.Exit(0)
		}

		switch os.Getenv("CLUSTER_TYPE") {
		case "async":
			if err := checkLivenessAsync(ctx); err != nil {
				log.Fatalf("liveness check failed: %v", err)
			}
		case "group-replication":
			if err := checkLivenessGR(ctx); err != nil {
				log.Fatalf("liveness check failed: %v", err)
			}
		}
	case "replication":
		if err := checkReplication(ctx); err != nil {
			log.Fatalf("replication check failed: %v", err)
		}
	default:
		log.Fatalf("Usage: %s liveness|readiness|replication", os.Args[0])
	}
}

func checkReadinessAsync(ctx context.Context) error {
	podIP, err := getPodIP()
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}

	monitorPass, err := getSecret(string(apiv1alpha1.UserMonitor))
	if err != nil {
		return errors.Wrapf(err, "get %s password", apiv1alpha1.UserMonitor)
	}

	params := database.DBParams{
		User: apiv1alpha1.UserMonitor,
		Pass: monitorPass,
		Host: podIP,
	}
	db, err := database.NewDatabase(ctx, params)
	if err != nil {
		return errors.Wrap(err, "connect to db")
	}
	defer db.Close()

	readOnly, err := db.IsReadonly(ctx)
	if err != nil {
		return errors.Wrap(err, "check read only status")
	}

	// if isReplica is true, replication is active
	isReplica, err := db.IsReplica(ctx)
	if err != nil {
		return errors.Wrap(err, "check replica status")
	}

	if isReplica && !readOnly {
		return errors.New("replica is not read only")
	}

	return nil
}

func checkReadinessGR(ctx context.Context) error {
	podIP, err := getPodIP()
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}

	monitorPass, err := getSecret(string(apiv1alpha1.UserMonitor))
	if err != nil {
		return errors.Wrapf(err, "get %s password", apiv1alpha1.UserMonitor)
	}

	params := database.DBParams{
		User: apiv1alpha1.UserMonitor,
		Pass: monitorPass,
		Host: podIP,
	}
	db, err := database.NewDatabase(ctx, params)
	if err != nil {
		return errors.Wrap(err, "connect to db")
	}
	defer db.Close()

	fqdn, err := getPodFQDN(os.Getenv("SERVICE_NAME"))
	if err != nil {
		return errors.Wrap(err, "get pod hostname")
	}

	state, err := db.GetMemberState(ctx, fqdn)
	if err != nil {
		return errors.Wrap(err, "get member state")
	}

	if state != mysqldb.MemberStateOnline {
		return errors.Errorf("Member state: %s", state)
	}

	return nil
}

func checkLivenessAsync(ctx context.Context) error {
	podIP, err := getPodIP()
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}

	monitorPass, err := getSecret(string(apiv1alpha1.UserMonitor))
	if err != nil {
		return errors.Wrapf(err, "get %s password", apiv1alpha1.UserMonitor)
	}

	params := database.DBParams{
		User: apiv1alpha1.UserMonitor,
		Pass: monitorPass,
		Host: podIP,
	}
	db, err := database.NewDatabase(ctx, params)
	if err != nil {
		return errors.Wrap(err, "connect to db")
	}
	defer db.Close()

	return db.DumbQuery(ctx)
}

func checkLivenessGR(ctx context.Context) error {
	podIP, err := getPodIP()
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}

	monitorPass, err := getSecret(string(apiv1alpha1.UserMonitor))
	if err != nil {
		return errors.Wrapf(err, "get %s password", apiv1alpha1.UserMonitor)
	}

	params := database.DBParams{
		User: apiv1alpha1.UserMonitor,
		Pass: monitorPass,
		Host: podIP,
	}
	db, err := database.NewDatabase(ctx, params)
	if err != nil {
		return errors.Wrap(err, "connect to db")
	}
	defer db.Close()

	in, err := db.CheckIfInPrimaryPartition(ctx)
	if err != nil {
		return errors.Wrap(err, "check if member in primary partition")
	}

	log.Printf("in primary partition: %t", in)

	if !in {
		return errors.New("possible split brain!")
	}

	return nil
}

func checkReplication(ctx context.Context) error {
	podIP, err := getPodIP()
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}

	monitorPass, err := getSecret(string(apiv1alpha1.UserMonitor))
	if err != nil {
		return errors.Wrapf(err, "get %s password", apiv1alpha1.UserMonitor)
	}

	params := database.DBParams{
		User: apiv1alpha1.UserMonitor,
		Pass: monitorPass,
		Host: podIP,
	}
	db, err := database.NewDatabase(ctx, params)
	if err != nil {
		return errors.Wrap(err, "connect to db")
	}
	defer db.Close()

	// if isReplica is true, replication is active
	isReplica, err := db.IsReplica(ctx)
	if err != nil {
		return errors.Wrap(err, "check replica status")
	}

	if !isReplica {
		return errors.New("replication is not active")
	}

	return nil
}

func getSecret(username string) (string, error) {
	path := filepath.Join(mysql.CredsMountPath, username)
	sBytes, err := os.ReadFile(path)
	if err != nil {
		return "", errors.Wrapf(err, "read %s", path)
	}

	return strings.TrimSpace(string(sBytes)), nil
}

func getPodHostname() (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", errors.Wrap(err, "get hostname")
	}

	return hostname, nil
}

func getPodIP() (string, error) {
	hostname, err := getPodHostname()
	if err != nil {
		return "", err
	}

	addrs, err := net.LookupHost(hostname)
	if err != nil {
		return "", errors.Wrapf(err, "lookup %s", hostname)
	}

	return addrs[0], nil
}

func getPodFQDN(svcName string) (string, error) {
	hostname, err := getPodHostname()
	if err != nil {
		return "", err
	}

	namespace, err := k8s.DefaultAPINamespace()
	if err != nil {
		return "", errors.Wrap(err, "get namespace")
	}

	return fmt.Sprintf("%s.%s.%s", hostname, svcName, namespace), nil
}

func fileExists(name string) (bool, error) {
	_, err := os.Stat(name)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, errors.Wrap(err, "os stat")
	}
	return true, nil
}
