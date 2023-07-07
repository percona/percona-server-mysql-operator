package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/sjmudd/stopwatch"
	"k8s.io/apimachinery/pkg/util/sets"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
)

func bootstrapAsyncReplication() error {
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
	primary, replicas, err := getTopology(peers)
	if err != nil {
		return errors.Wrap(err, "select donor")
	}
	log.Printf("Primary: %s Replicas: %v", primary, replicas)

	fqdn, err := getFQDN(mysqlSvc)
	if err != nil {
		return errors.Wrap(err, "get FQDN")
	}
	log.Printf("FQDN: %s", fqdn)

	podHostname, err := os.Hostname()
	if err != nil {
		return errors.Wrap(err, "get hostname")
	}

	podIp, err := getPodIP(podHostname)
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}
	log.Printf("PodIP: %s", podIp)

	primaryIp, err := getPodIP(primary)
	if err != nil {
		return errors.Wrap(err, "get primary IP")
	}
	log.Printf("PrimaryIP: %s", primaryIp)

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
	case primary == fqdn || primaryIp == podIp:
		if err := db.ResetReplication(); err != nil {
			return err
		}

		log.Printf("I'm the primary.")
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

func getTopology(peers sets.Set[string]) (string, []string, error) {
	replicas := sets.New[string]()
	primary := ""

	operatorPass, err := getSecret(apiv1alpha1.UserOperator)
	if err != nil {
		return "", nil, errors.Wrapf(err, "get %s password", apiv1alpha1.UserOperator)
	}

	for _, peer := range sets.List(peers) {
		db, err := replicator.NewReplicator("operator", operatorPass, peer, mysql.DefaultAdminPort)
		if err != nil {
			return "", nil, errors.Wrapf(err, "connect to %s", peer)
		}
		defer db.Close()

		status, source, err := db.ReplicationStatus()
		if err != nil {
			return "", nil, errors.Wrap(err, "check replication status")
		}

		replicaHost, err := db.ReportHost()
		if err != nil {
			return "", nil, errors.Wrap(err, "get report_host")
		}
		if replicaHost == "" {
			continue
		}
		replicas.Insert(replicaHost)

		if status == replicator.ReplicationStatusActive {
			primary = source
		}
	}

	if primary == "" && peers.Len() == 1 {
		primary = sets.List(peers)[0]
	} else if primary == "" {
		primary = sets.List(replicas)[0]
	}

	if replicas.Len() > 0 {
		replicas.Delete(primary)
	}

	return primary, sets.List(replicas), nil
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
