package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
)

func bootstrapGroupReplication() error {
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

	podHostname, err := os.Hostname()
	if err != nil {
		return errors.Wrap(err, "get hostname")
	}

	podIp, err := getPodIP(podHostname)
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}
	log.Printf("PodIP: %s", podIp)

	log.Printf("Opening connection to %s", podIp)
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

	peers, err := lookup(os.Getenv("SERVICE_NAME"))
	if err != nil {
		return errors.Wrap(err, "lookup")
	}
	log.Printf("Peers: %v", peers.List())

	seeds, err := getGroupSeeds(peers)
	if err != nil {
		return errors.Wrap(err, "get group seeds")
	}
	seedsStr := strings.Join(seeds, ",")

	if err := db.SetGlobal("group_replication_group_seeds", seedsStr); err != nil {
		return err
	}
	log.Printf("set group_replication_group_seeds: %s", seedsStr)

	if strings.HasSuffix(podHostname, "0") && peers.Len() == 1 {
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
		log.Printf("There is only 1 peer: %s", peer)
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
			return nil, errors.Wrapf(err, "connect to %s", peer)
		}
		defer db.Close()

		p, err := db.GetGroupReplicationPrimary()
		if err != nil {
			return nil, errors.Wrapf(err, "get primary from %s", peer)
		}
		log.Printf("Found primary: %s", p)

		primary = p
		break
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
