package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

const (
	fullClusterCrashFile = "/var/lib/mysql/full-cluster-crash"
	manualRecoveryFile   = "/var/lib/mysql/sleep-forever"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	f, err := os.OpenFile(filepath.Join(mysql.DataMountPath, "bootstrap.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	defer f.Close()
	log.SetOutput(f)

	fullClusterCrash, err := fileExists(fullClusterCrashFile)
	if err == nil && fullClusterCrash {
		log.Printf("%s exists. exiting...", fullClusterCrashFile)
		os.Exit(0)
	}

	manualRecovery, err := fileExists(manualRecoveryFile)
	if err == nil && manualRecovery {
		log.Printf("%s exists. exiting...", manualRecoveryFile)
		os.Exit(0)
	}

	clusterType := os.Getenv("CLUSTER_TYPE")
	switch clusterType {
	case "group-replication":
		if err := bootstrapGroupReplication(context.Background()); err != nil {
			log.Fatalf("bootstrap failed: %v", err)
		}
	case "async":
		if err := bootstrapAsyncReplication(ctx); err != nil {
			log.Fatalf("bootstrap failed: %v", err)
		}
	default:
		log.Fatalf("Invalid cluster type: %v", clusterType)
	}
}
