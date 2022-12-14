package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/pkg/errors"
	"github.com/sjmudd/stopwatch"
	"k8s.io/apimachinery/pkg/util/sets"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
)

var ErrNoPrimaryInCluster = errors.New("primary not found in cluster")

func bootstrapGroupReplication() error {
	timer := stopwatch.NewNamedStopwatch()
	err := timer.Add("total")
	if err != nil {
		return errors.Wrap(err, "add timer")
	}
	timer.Start("total")

	defer func() {
		timer.Stop("total")
		log.Printf("bootstrap finished in %f seconds", timer.ElapsedSeconds("total"))
	}()

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

	exists, err = checkFullClusterCrash()
	if err != nil {
		return errors.Wrap(err, "check full cluster crash file")
	}
	if exists {
		log.Printf("/var/lib/mysql/full-cluster-crash exists. exiting...")
		return nil
	}

	podHostname, err := os.Hostname()
	if err != nil {
		return errors.Wrap(err, "get hostname")
	}

	podIp, err := getPodIP(podHostname)
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}
	log.Printf("Pod IP: %s", podIp)

	log.Printf("opening connection to %s", podIp)
	operatorPass, err := getSecret(apiv1alpha1.UserOperator)
	if err != nil {
		return errors.Wrapf(err, "get %s password", apiv1alpha1.UserOperator)
	}

	db, err := replicator.NewReplicator(apiv1alpha1.UserOperator, operatorPass, podIp, mysql.DefaultAdminPort)
	if err != nil {
		return errors.Wrap(err, "connect to db")
	}
	defer db.Close()

	crUID := os.Getenv("CR_UID")
	if err := db.SetGlobal("group_replication_group_name", crUID); err != nil {
		return err
	}
	log.Printf("set group_replication_group_name: %v", crUID)

	fqdn, err := getFQDN(os.Getenv("SERVICE_NAME"))
	if err != nil {
		return errors.Wrap(err, "get FQDN")
	}
	log.Printf("FQDN: %s", fqdn)

	localAddress := fmt.Sprintf("%s:%d", fqdn, mysql.DefaultGRPort)
	if err := db.SetGlobal("group_replication_local_address", localAddress); err != nil {
		return err
	}
	log.Printf("set group_replication_local_address: %s", localAddress)

	forceBootstrap, err := checkForceBootstrap()
	if err != nil {
		return errors.Wrap(err, "check force bootstrap file")
	}
	log.Printf("check if /var/lib/mysql/force-bootstrap exists: %t", forceBootstrap)

	var seeds []string
	if forceBootstrap {
		seeds = []string{fmt.Sprintf("%s:%d", fqdn, mysql.DefaultGRPort)}
	} else {
		peers, err := lookup(os.Getenv("SERVICE_NAME"))
		if err != nil {
			return errors.Wrap(err, "lookup")
		}
		log.Printf("peers: %v", peers.List())

		seeds, err = getGroupSeeds(peers)
		if err != nil {
			if errors.Is(err, ErrNoPrimaryInCluster) {
				log.Printf("CRITICAL: %s, we're in a full cluster crash", ErrNoPrimaryInCluster)

				gtidExecuted, err := db.GetGlobal("GTID_EXECUTED")
				if err != nil {
					return err
				}

				log.Printf("GTID_EXECUTED: %s", gtidExecuted)

				if err := createFile("/var/lib/mysql/full-cluster-crash", fmt.Sprintf("%s", gtidExecuted)); err != nil {
					return err
				}

				return errors.New("full cluster crash")
			}
			return errors.Wrap(err, "get group seeds")
		}
	}
	seedsStr := strings.Join(seeds, ",")

	if err := db.SetGlobal("group_replication_group_seeds", seedsStr); err != nil {
		return err
	}
	log.Printf("set group_replication_group_seeds: %s", seedsStr)

	bootstrap, err := shouldIBootstrap(db, forceBootstrap)
	if err != nil {
		return errors.Wrap(err, "check if I need to bootstrap")
	}

	if bootstrap {
		if err := db.SetGlobal("group_replication_bootstrap_group", "ON"); err != nil {
			return err
		}
		log.Println("set group_replication_bootstrap_group: ON")
	}

	replicationPass, err := getSecret(apiv1alpha1.UserReplication)
	if err != nil {
		return errors.Wrapf(err, "get %s password", apiv1alpha1.UserReplication)
	}

	if err := db.StartGroupReplication(replicationPass); err != nil {
		return err
	}
	log.Printf("started group replication")

	if err := db.SetGlobal("group_replication_bootstrap_group", "OFF"); err != nil {
		return err
	}
	log.Println("set group_replication_bootstrap_group: OFF")

	return nil
}

func getGroupSeeds(peers sets.String) ([]string, error) {
	seeds := sets.NewString()

	if peers.Len() == 1 {
		peer := peers.List()[0]
		log.Printf("there is only 1 peer: %s", peer)
		seeds.Insert(fmt.Sprintf("%s:%d", peer, mysql.DefaultGRPort))
		return seeds.List(), nil
	}

	operatorPass, err := getSecret(apiv1alpha1.UserOperator)
	if err != nil {
		return nil, errors.Wrapf(err, "get %s password", apiv1alpha1.UserOperator)
	}

	podHostname, err := os.Hostname()
	if err != nil {
		return nil, errors.Wrap(err, "get hostname")
	}

	// Find primary
	var primary string
	for _, peer := range peers.List() {
		if strings.HasPrefix(peer, podHostname) {
			continue
		}

		log.Printf("connecting to peer %s", peer)
		db, err := replicator.NewReplicator(apiv1alpha1.UserOperator, operatorPass, peer, mysql.DefaultAdminPort)
		if err != nil {
			log.Printf("ERROR: connect to %s: %v", peer, err)
			continue
		}
		defer db.Close()

		p, err := db.GetGroupReplicationPrimary()
		if err != nil {
			log.Printf("ERROR: get primary from %s: %v", peer, err)
			continue
		}

		log.Printf("found primary: %s", p)
		primary = p
		break
	}

	if primary == "" {
		return nil, ErrNoPrimaryInCluster
	}

	log.Printf("connecting to primary %s", primary)
	db, err := replicator.NewReplicator(apiv1alpha1.UserOperator, operatorPass, primary, mysql.DefaultAdminPort)
	if err != nil {
		return nil, errors.Wrapf(err, "connect to primary %s", primary)
	}
	defer db.Close()

	replicas, err := db.GetGroupReplicationReplicas()
	if err != nil {
		return nil, errors.Wrapf(err, "query replicas from %s", primary)
	}

	seeds.Insert(fmt.Sprintf("%s:%d", primary, mysql.DefaultGRPort))
	for _, replica := range replicas {
		seeds.Insert(fmt.Sprintf("%s:%d", replica, mysql.DefaultGRPort))
	}

	return seeds.List(), nil
}

func checkFullClusterCrash() (bool, error) {
	return fileExists("/var/lib/mysql/full-cluster-crash")
}

func checkForceBootstrap() (bool, error) {
	return fileExists("/var/lib/mysql/force-bootstrap")
}

func shouldIBootstrap(db replicator.Replicator, forceBootstrap bool) (bool, error) {
	podHostname, err := os.Hostname()
	if err != nil {
		return false, errors.Wrap(err, "get hostname")
	}

	alreadyBootstrapped, err := db.CheckIfDatabaseExists("mysql_innodb_cluster_metadata")
	if err != nil {
		return false, errors.Wrap(err, "check if database mysql_innodb_cluster_metadata exists")
	}
	log.Printf("check if InnoDB Cluster is already bootstrapped: %t", alreadyBootstrapped)

	switch {
	case forceBootstrap:
		log.Printf("WARNING: FORCING BOOTSTRAP")
		if err := os.Remove("/var/lib/mysql/force-bootstrap"); err != nil {
			log.Printf("ERROR: remove /var/lib/mysql/force-bootstrap: %s", err)
		}
		return true, nil
	case !alreadyBootstrapped && strings.HasSuffix(podHostname, "0"):
		log.Printf("group is not bootstrapped yet, bootstrapping")
		return true, nil
	default:
		return false, nil
	}
}

func createFile(name, content string) error {
	f, err := os.Create(name)
	if err != nil {
		return errors.Wrapf(err, "create %s", name)
	}

	n, err := f.WriteString(content)
	if err != nil {
		return errors.Wrapf(err, "write to %s", name)
	}

	log.Printf("written %d bytes to %s", n, name)

	return nil
}
