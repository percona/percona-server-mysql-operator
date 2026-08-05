package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/pkg/errors"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/cmd/bootstrap/async"
	"github.com/percona/percona-server-mysql-operator/cmd/bootstrap/gr"
	"github.com/percona/percona-server-mysql-operator/cmd/bootstrap/utils"
	database "github.com/percona/percona-server-mysql-operator/cmd/internal/db"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

func main() {
	f, err := os.OpenFile(filepath.Join(mysql.DataMountPath, "bootstrap.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	defer f.Close()

	log.SetOutput(io.MultiWriter(os.Stderr, f))

	if err := utils.CheckClustersetRecovery(); err != nil {
		log.Fatalf("failed to check clusterset recovery: %v", err)
	}

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

	ctx := context.Background()

	if err := installComponents(ctx); err != nil {
		log.Fatalf("failed to install components: %v", err)
	}

	clusterType := os.Getenv("CLUSTER_TYPE")
	switch clusterType {
	case "group-replication":
		if err := gr.Bootstrap(ctx); err != nil {
			log.Fatalf("bootstrap failed: %v", err)
		}
	case "async":
		if err := async.Bootstrap(ctx); err != nil {
			log.Fatalf("bootstrap failed: %v", err)
		}
	default:
		log.Fatalf("Invalid cluster type: %v", clusterType)
	}
}

func installComponents(ctx context.Context) error {
	podHostname, err := os.Hostname()
	if err != nil {
		return errors.Wrap(err, "get hostname")
	}

	podIp, err := utils.GetPodIP(podHostname)
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}

	operatorPass, err := utils.GetSecret(apiv1.UserOperator)
	if err != nil {
		return errors.Wrapf(err, "get %s password", apiv1.UserOperator)
	}

	params := database.DBParams{
		User: apiv1.UserOperator,
		Pass: operatorPass,
		Host: podIp,
	}

	db, err := database.NewDatabase(ctx, params)
	if err != nil {
		return errors.Wrap(err, "connect to MySQL")
	}
	defer db.Close()

	if err := db.InstallComponentMySQLBackup(ctx); err != nil {
		return errors.Wrap(err, "install components")
	}

	return nil
}
