package main

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/sjmudd/stopwatch"
	"k8s.io/apimachinery/pkg/util/sets"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql/topology"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
)

func bootstrapAsyncReplication(ctx context.Context) error {
	timer := stopwatch.NewNamedStopwatch()
	err := timer.AddMany([]string{"clone", "total"})
	if err != nil {
		return errors.Wrap(err, "add timers")
	}
	timer.Start("total")

	defer func() {
		timer.Stop("total")
		log.Printf("bootstrap finished in %f seconds", timer.ElapsedSeconds("total"))
	}()

	svc := os.Getenv("SERVICE_NAME_UNREADY")
	mysqlSvc := os.Getenv("SERVICE_NAME")
	peers, err := lookup(svc)
	if err != nil {
		return errors.Wrap(err, "lookup")
	}
	log.Printf("Peers: %v", sets.List(peers))

	exists, err := lockExists()
	if err != nil {
		return errors.Wrap(err, "lock file check")
	}
	if exists {
		log.Printf("Waiting for bootstrap.lock to be deleted")
		if err = waitLockRemoval(); err != nil {
			return errors.Wrap(err, "wait lock removal")
		}
	}
	fqdn, err := getFQDN(mysqlSvc)
	if err != nil {
		return errors.Wrap(err, "get FQDN")
	}
	log.Printf("FQDN: %s", fqdn)

	primary, replicas, err := getTopology(ctx, fqdn, peers)
	if err != nil {
		return errors.Wrap(err, "get topology")
	}
	log.Printf("Primary: %s Replicas: %v", primary, replicas)

	podHostname, err := os.Hostname()
	if err != nil {
		return errors.Wrap(err, "get hostname")
	}

	podIp, err := getPodIP(podHostname)
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}
	log.Printf("PodIP: %s", podIp)

	donor, err := selectDonor(fqdn, primary, replicas)
	if err != nil {
		return errors.Wrap(err, "select donor")
	}
	log.Printf("Donor: %s", donor)

	log.Printf("Opening connection to %s", podIp)
	operatorPass, err := getSecret(apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrapf(err, "get %s password", apiv1alpha1.UserOperator)
	}

	db, err := replicator.NewReplicator("operator", operatorPass, podIp, mysql.DefaultAdminPort)
	if err != nil {
		return errors.Wrap(err, "connect to db")
	}
	defer db.Close()

	if err := db.StopReplication(); err != nil {
		return err
	}

	switch {
	case primary == fqdn:
		if err := db.ResetReplication(); err != nil {
			return err
		}

		log.Printf("I'm the primary.")
		return nil
	case donor == "":
		if err := db.ResetReplication(); err != nil {
			return err
		}

		log.Printf("Can't find a donor, we're on our own.")
		return nil
	case donor == fqdn:
		if err := db.ResetReplication(); err != nil {
			return err
		}

		log.Printf("I'm the donor and therefore the primary.")
		return nil
	}

	cloneLock := filepath.Join(mysql.DataMountPath, "clone.lock")
	requireClone, err := isCloneRequired(cloneLock)
	if err != nil {
		return errors.Wrap(err, "check if clone is required")
	}

	log.Printf("Clone required: %t", requireClone)
	if requireClone {
		log.Println("Checking if a clone in progress")
		inProgress, err := db.CloneInProgress()
		if err != nil {
			return errors.Wrap(err, "check if a clone in progress")
		}

		log.Printf("Clone in progress: %t", inProgress)
		if inProgress {
			return nil
		}

		timer.Start("clone")
		log.Printf("Cloning from %s", donor)
		err = db.Clone(donor, "operator", operatorPass, mysql.DefaultAdminPort)
		timer.Stop("clone")
		if err != nil && !errors.Is(err, replicator.ErrRestartAfterClone) {
			return errors.Wrapf(err, "clone from donor %s", donor)
		}

		err = createCloneLock(cloneLock)
		if err != nil {
			return errors.Wrap(err, "create clone lock")
		}

		log.Println("Clone finished. Restarting container...")

		// We return with 1 to restart container
		os.Exit(1)
	}

	if !requireClone {
		if err := deleteCloneLock(cloneLock); err != nil {
			return errors.Wrap(err, "delete clone lock")
		}
	}

	rStatus, _, err := db.ReplicationStatus()
	if err != nil {
		return errors.Wrap(err, "check replication status")
	}

	if rStatus == replicator.ReplicationStatusNotInitiated {
		log.Println("configuring replication")

		replicaPass, err := getSecret(apiv1alpha1.UserReplication)
		if err != nil {
			return errors.Wrapf(err, "get %s password", apiv1alpha1.UserReplication)
		}

		if err := db.StopReplication(); err != nil {
			return errors.Wrap(err, "stop replication")
		}

		if err := db.StartReplication(primary, replicaPass, mysql.DefaultPort); err != nil {
			return errors.Wrap(err, "start replication")
		}
	}

	if err := db.EnableSuperReadonly(); err != nil {
		return errors.Wrap(err, "enable super read only")
	}

	return nil
}

func getTopology(ctx context.Context, fqdn string, peers sets.Set[string]) (string, []string, error) {
	operatorPass, err := getSecret(apiv1alpha1.UserOperator)
	if err != nil {
		return "", nil, errors.Wrapf(err, "get %s password", apiv1alpha1.UserOperator)
	}
	t, err := topology.GetAsync(ctx, operatorPass, sets.List(peers)...)
	if err != nil {
		return "", nil, errors.Wrap(err, "failed to get topology")
	}

	// When there are only 2 MySQL pods and one of them is not bootstrapped `GetAsync` returns first replica as a primary
	// There could be a case when this primary is actually a bootstrapping pod
	// To avoid that, we should swap first replica with the primary
	if t.Primary == fqdn && len(t.Replicas) >= 1 {
		t.SetPrimary(t.Replicas[0])
		t.AddReplica(fqdn)
	}

	return t.Primary, t.Replicas, nil
}

func selectDonor(fqdn, primary string, replicas []string) (string, error) {
	donor := ""

	operatorPass, err := getSecret(apiv1alpha1.UserOperator)
	if err != nil {
		return "", errors.Wrapf(err, "get %s password", apiv1alpha1.UserOperator)
	}

	for _, replica := range replicas {
		db, err := replicator.NewReplicator("operator", operatorPass, replica, mysql.DefaultAdminPort)
		if err != nil {
			continue
		}
		db.Close()

		if fqdn != replica {
			donor = replica
			break
		}
	}

	if donor == "" && fqdn != primary {
		donor = primary
	}

	return donor, nil
}

func isCloneRequired(file string) (bool, error) {
	_, err := os.Stat(file)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, errors.Wrapf(err, "stat %s", file)
	}

	return false, nil
}

func createCloneLock(file string) error {
	_, err := os.Create(file)
	return errors.Wrapf(err, "create %s", file)
}

func deleteCloneLock(file string) error {
	err := os.Remove(file)
	return errors.Wrapf(err, "remove %s", file)
}
