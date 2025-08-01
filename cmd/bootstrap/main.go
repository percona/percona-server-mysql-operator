package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

const (
	noBootstrapFile      = "/var/lib/mysql/no-bootstrap"
	manualRecoveryFile   = "/var/lib/mysql/sleep-forever"
	fullClusterCrashFile = "/var/lib/mysql/full-cluster-crash"
)

func main() {
	f, err := os.OpenFile(filepath.Join(mysql.DataMountPath, "bootstrap.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	defer f.Close()

	log.SetOutput(io.MultiWriter(os.Stderr, f))

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

	exists, err := lockExists("bootstrap")
	if err != nil {
		log.Fatalf("failed to check bootstrap.lock: %s", err)
	}
	if exists {
		log.Printf("Waiting for bootstrap.lock to be deleted")
		if err = waitLockRemoval("bootstrap"); err != nil {
			log.Fatalf("failed to wait for bootstrap.lock: %s", err)
		}
	}

	log.Println("Waiting for MySQL ready state")
	if err := waitForMySQLReadyState(); err != nil {
		log.Fatalf("Failed to wait for ready MySQL state: %s", err)
	}
	log.Println("MySQL is ready")

	clusterType := os.Getenv("CLUSTER_TYPE")
	switch clusterType {
	case "group-replication":
		if err := bootstrapGroupReplication(context.Background()); err != nil {
			log.Fatalf("bootstrap failed: %v", err)
		}
	case "async":
		if err := bootstrapAsyncReplication(context.Background()); err != nil {
			log.Fatalf("bootstrap failed: %v", err)
		}
	default:
		log.Fatalf("Invalid cluster type: %v", clusterType)
	}
}
