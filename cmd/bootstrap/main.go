package main

import (
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/sjmudd/stopwatch"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/percona/percona-server-mysql-operator/pkg/database/mysql"
)

const (
	mysqlPort      = int32(3306)
	mysqlAdminPort = int32(33062)
)

func main() {
	datadir := os.Getenv("MYSQL_DATA_DIR")
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
	err := timer.AddMany([]string{"clone", "total"})
	if err != nil {
		return errors.Wrap(err, "add timers")
	}
	timer.Start("total")

	defer func() {
		timer.Stop("total")
		log.Printf("bootstrap finished in %f seconds", timer.ElapsedSeconds("total"))
	}()

	svc := os.Getenv("SERVICE_NAME")
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

	fqdn := os.Getenv("FQDN")
	log.Printf("FQDN: %s", fqdn)

	donor := selectDonor(fqdn, primary, replicas)
	log.Printf("Donor: %s", donor)

	if donor == "" || donor == fqdn || primary == fqdn {
		return nil
	}

	podIP := os.Getenv("POD_IP")
	log.Printf("Opening connection to %s", podIP)
	operatorPass := os.Getenv("OPERATOR_ADMIN_PASSWORD")
	db, err := mysql.NewConnection("operator", operatorPass, podIP, mysqlAdminPort)
	if err != nil {
		return errors.Wrap(err, "connect to db")
	}
	defer db.Close()

	needsClone, err := db.NeedsClone(donor, mysqlAdminPort)
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
		err = db.Clone(donor, "operator", operatorPass, mysqlAdminPort)
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
		if err := db.StartReplication(primary, replicaPass, mysqlPort); err != nil {
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
		// The SRV records have the pattern $HOSTNAME.$SERVICE.$.NAMESPACE.svc.$CLUSTER_DNS_SUFFIX
		// We only want $HOSTNAME.$SERVICE.$NAMESPACE
		srv := strings.Split(srvRecord.Target, ".")
		ep := strings.Join(srv[:3], ".")
		endpoints.Insert(ep)
	}
	return endpoints, nil
}

func getTopology(peers sets.String) (string, []string, error) {
	replicas := sets.NewString()
	primary := ""

	operatorPass := os.Getenv("OPERATOR_ADMIN_PASSWORD")
	for _, peer := range peers.List() {
		db, err := mysql.NewConnection("operator", operatorPass, peer, mysqlAdminPort)
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
		db, err := mysql.NewConnection("operator", operatorPass, replica, mysqlAdminPort)
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

	return donor
}
