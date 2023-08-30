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
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
)

// main evaluates cluster status and conducts health or replication checks.
func main() {
	fullClusterCrash, err := fileExists("/var/lib/mysql/full-cluster-crash")
	if err != nil {
		log.Fatalf("check /var/lib/mysql/full-cluster-crash: %s", err)
	}
	if fullClusterCrash {
		os.Exit(0)
	}

	manualRecovery, err := fileExists("/var/lib/mysql/sleep-forever")
	if err != nil {
		log.Fatalf("check /var/lib/mysql/sleep-forever: %s", err)
	}
	if manualRecovery {
		os.Exit(0)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch os.Args[1] {
	case "readiness":
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

// checkReadinessAsync verifies the asynchronous replication readiness of a MySQL pod.
func checkReadinessAsync(ctx context.Context) error {
	podIP, err := getPodIP()
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}

	monitorPass, err := getSecret(string(apiv1alpha1.UserMonitor))
	if err != nil {
		return errors.Wrapf(err, "get %s password", apiv1alpha1.UserMonitor)
	}

	db, err := replicator.NewReplicator(ctx, apiv1alpha1.UserMonitor, monitorPass, podIP, mysql.DefaultAdminPort)
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

// checkReadinessGR validates the readiness state of a MySQL pod in group replication.
func checkReadinessGR(ctx context.Context) error {
	podIP, err := getPodIP()
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}

	monitorPass, err := getSecret(string(apiv1alpha1.UserMonitor))
	if err != nil {
		return errors.Wrapf(err, "get %s password", apiv1alpha1.UserMonitor)
	}

	db, err := replicator.NewReplicator(ctx, apiv1alpha1.UserMonitor, monitorPass, podIP, mysql.DefaultAdminPort)
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

	if state != replicator.MemberStateOnline {
		return errors.Errorf("Member state: %s", state)
	}

	return nil
}

// checkLivenessAsync checks the liveness of an asynchronous MySQL replica.
func checkLivenessAsync(ctx context.Context) error {
	podIP, err := getPodIP()
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}

	monitorPass, err := getSecret(string(apiv1alpha1.UserMonitor))
	if err != nil {
		return errors.Wrapf(err, "get %s password", apiv1alpha1.UserMonitor)
	}

	db, err := replicator.NewReplicator(ctx, apiv1alpha1.UserMonitor, monitorPass, podIP, mysql.DefaultAdminPort)
	if err != nil {
		return errors.Wrap(err, "connect to db")
	}
	defer db.Close()

	return db.DumbQuery(ctx)
}

// checkLivenessGR verifies the liveness of a group replication MySQL instance.
func checkLivenessGR(ctx context.Context) error {
	podIP, err := getPodIP()
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}

	monitorPass, err := getSecret(string(apiv1alpha1.UserMonitor))
	if err != nil {
		return errors.Wrapf(err, "get %s password", apiv1alpha1.UserMonitor)
	}

	db, err := replicator.NewReplicator(ctx, apiv1alpha1.UserMonitor, monitorPass, podIP, mysql.DefaultAdminPort)
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

// checkReplication validates if the MySQL instance is actively replicating.
func checkReplication(ctx context.Context) error {
	podIP, err := getPodIP()
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}

	monitorPass, err := getSecret(string(apiv1alpha1.UserMonitor))
	if err != nil {
		return errors.Wrapf(err, "get %s password", apiv1alpha1.UserMonitor)
	}

	db, err := replicator.NewReplicator(ctx, apiv1alpha1.UserMonitor, monitorPass, podIP, mysql.DefaultAdminPort)
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

// getSecret retrieves the secret for a given username from the mounted path.
func getSecret(username string) (string, error) {
	path := filepath.Join(mysql.CredsMountPath, username)
	sBytes, err := os.ReadFile(path)
	if err != nil {
		return "", errors.Wrapf(err, "read %s", path)
	}

	return strings.TrimSpace(string(sBytes)), nil
}

// getPodHostname gets the hostname of the current pod.
func getPodHostname() (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", errors.Wrap(err, "get hostname")
	}

	return hostname, nil
}

// getPodIP fetches the IP address of the current pod.
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

// getPodFQDN computes the Fully Qualified Domain Name
// for the current pod within its service context.
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

// fileExists checks if a file exists at the specified path.
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
