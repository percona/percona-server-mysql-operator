package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/sjmudd/stopwatch"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/percona/percona-server-mysql-operator/pkg/database/mysql"
)

func main() {
	datadir := os.Getenv("MY_DATA_DIR")
	f, err := os.OpenFile(filepath.Join(datadir, "bootstrap.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	defer f.Close()
	log.SetOutput(f)

	if err := bootstrap(); err != nil {
		log.Fatalf("bootstrap failed: %v", err)
	}
}

func bootstrap() error {
	timer := stopwatch.NewNamedStopwatch()
	timer.AddMany([]string{"clone", "total"})
	timer.Start("total")

	defer func() {
		timer.Stop("total")
		log.Printf("bootstrap finished in %f seconds", timer.ElapsedSeconds("total"))
	}()

	svc := os.Getenv("MY_SERVICE_NAME")
	peers, err := lookup(svc)
	if err != nil {
		return errors.Wrap(err, "lookup")
	}
	log.Printf("Peers: %v", peers.List())

	primary, replicas, err := getTopology(peers)
	if err != nil {
		return errors.Wrap(err, "select donor")
	}
	log.Printf("Primary: %s Replicas: %v", primary, replicas)

	// TODO: Don't hardcode cluster dns suffix
	fqdn := fmt.Sprintf("%s.svc.cluster.local", os.Getenv("MY_FQDN"))
	log.Printf("MY_FQDN: %s", fqdn)

	donor := selectDonor(fqdn, primary, replicas)
	log.Printf("Donor: %s", donor)

	if donor == "" || donor == fqdn || primary == fqdn {
		return nil
	}

	log.Println("Opening connection to 127.0.0.1")
	operatorPass := os.Getenv("OPERATOR_ADMIN_PASSWORD")
	db, err := mysql.NewConnection("operator", operatorPass, "127.0.0.1", int32(3306))
	if err != nil {
		return errors.Wrap(err, "connect to db")
	}
	defer db.Close()

	needsClone, err := db.NeedsClone(donor, int32(3306))
	if err != nil {
		return errors.Wrap(err, "check if a clone is needed")
	}

	log.Printf("Clone needed: %t", needsClone)
	if needsClone {
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
		err = db.Clone(donor, "operator", operatorPass, int32(3306))
		timer.Stop("clone")
		log.Printf("Clone finished in %f seconds", timer.ElapsedSeconds("clone"))
		if err != nil {
			return errors.Wrapf(err, "clone from donor %s", donor)
		}
	}

	rStatus, _, err := db.ReplicationStatus()
	if err != nil {
		return errors.Wrap(err, "check replication status")
	}

	if err := db.EnableReadonly(); err != nil {
		return errors.Wrap(err, "set read only")
	}

	if rStatus == mysql.ReplicationStatusNotInitiated {
		log.Println("configuring replication")
		replicaPass := os.Getenv("REPLICATION_PASSWORD")
		if err := db.StartReplication(primary, replicaPass, int32(3306)); err != nil {
			return errors.Wrap(err, "start replication")
		}
	}

	return nil
}

func lookup(svcName string) (sets.String, error) {
	endpoints := sets.NewString()
	_, srvRecords, err := net.LookupSRV("", "", svcName)
	if err != nil {
		return endpoints, err
	}
	for _, srvRecord := range srvRecords {
		// The SRV records ends in a "." for the root domain
		ep := fmt.Sprintf("%v", srvRecord.Target[:len(srvRecord.Target)-1])
		endpoints.Insert(ep)
	}
	return endpoints, nil
}

func getTopology(peers sets.String) (string, []string, error) {
	replicas := sets.NewString()
	primary := ""

	operatorPass := os.Getenv("OPERATOR_ADMIN_PASSWORD")
	for _, peer := range peers.List() {
		db, err := mysql.NewConnection("operator", operatorPass, peer, int32(3306))
		if err != nil {
			return "", nil, errors.Wrapf(err, "connect to %s", peer)
		}
		defer db.Close()

		status, source, err := db.ReplicationStatus()
		if err != nil {
			return "", nil, errors.Wrap(err, "check replication status")
		}

		if status == mysql.ReplicationStatusActive {
			replicas.Insert(peer)
			primary = source
		}
	}

	if primary == "" {
		primary = peers.List()[0]
	}

	return primary, replicas.List(), nil
}

func selectDonor(fqdn, primary string, replicas []string) string {
	operatorPass := os.Getenv("OPERATOR_ADMIN_PASSWORD")
	donor := ""

	for _, replica := range replicas {
		db, err := mysql.NewConnection("operator", operatorPass, replica, int32(3306))
		if err != nil {
			continue
		}
		defer db.Close()

		if fqdn != replica {
			donor = replica
			break
		}
	}

	if donor == "" && fqdn != primary {
		donor = primary
	}

	return donor
}
