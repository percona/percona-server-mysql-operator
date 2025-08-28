package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/percona/percona-server-mysql-operator/cmd/bootstrap/async"
	"github.com/percona/percona-server-mysql-operator/cmd/bootstrap/gr"
	"github.com/percona/percona-server-mysql-operator/cmd/bootstrap/utils"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

func main() {
	f, err := os.OpenFile(filepath.Join(mysql.DataMountPath, "bootstrap.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	defer f.Close()

	log.SetOutput(io.MultiWriter(os.Stderr, f))

	requested, rFile := utils.ManualRecoveryRequested()
	if requested {
		log.Printf("%s exists. exiting...", rFile)
		os.Exit(0)
	}

	exists, err := utils.LockExists("bootstrap")
	if err != nil {
		log.Fatalf("failed to check bootstrap.lock: %s", err)
	}
	if exists {
		log.Printf("Waiting for bootstrap.lock to be deleted")
		if err = utils.WaitLockRemoval("bootstrap"); err != nil {
			log.Fatalf("failed to wait for bootstrap.lock: %s", err)
		}
	}

	log.Println("Waiting for MySQL ready state")
	if err := utils.WaitForMySQLReadyState(); err != nil {
		log.Fatalf("Failed to wait for ready MySQL state: %s", err)
	}
	log.Println("MySQL is ready")

	clusterType := os.Getenv("CLUSTER_TYPE")
	switch clusterType {
	case "group-replication":
		if err := gr.Bootstrap(context.Background()); err != nil {
			log.Fatalf("bootstrap failed: %v", err)
		}
	case "async":
		if err := async.Bootstrap(context.Background()); err != nil {
			log.Fatalf("bootstrap failed: %v", err)
		}
	default:
		log.Fatalf("Invalid cluster type: %v", clusterType)
	}
}
