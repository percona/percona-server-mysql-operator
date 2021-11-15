package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/sjmudd/stopwatch"
	"k8s.io/apimachinery/pkg/util/sets"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/database/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

func main() {
	f, err := os.OpenFile(filepath.Join(mysql.DataMountPath, "bootstrap.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
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

	svc := os.Getenv("SERVICE_NAME_UNREADY")
	mysqlSvc := os.Getenv("SERVICE_NAME")
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

	if donor == "" || donor == fqdn || primary == fqdn || primaryIp == podIp {
		return nil
	}

	log.Printf("Opening connection to %s", podIp)
	operatorPass, err := getSecret(v2.USERS_SECRET_KEY_OPERATOR)
	if err != nil {
		return errors.Wrapf(err, "get %s password", v2.USERS_SECRET_KEY_OPERATOR)
	}

	db, err := mysql.NewReplicator("operator", operatorPass, podIp, mysql.DefaultAdminPort)
	if err != nil {
		return errors.Wrap(err, "connect to db")
	}
	defer db.Close()

	needsClone, err := db.NeedsClone(donor, mysql.DefaultAdminPort)
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
		err = db.Clone(donor, "operator", operatorPass, mysql.DefaultAdminPort)
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

	if rStatus == mysql.ReplicationStatusNotInitiated {
		log.Println("configuring replication")

		replicaPass, err := getSecret(v2.USERS_SECRET_KEY_REPLICATION)
		if err != nil {
			return errors.Wrapf(err, "get %s password", v2.USERS_SECRET_KEY_REPLICATION)
		}

		if err := db.StartReplication(primary, replicaPass, mysql.DefaultPort); err != nil {
			return errors.Wrap(err, "start replication")
		}
	}

	return nil
}

func getFQDN(svcName string) (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", errors.Wrap(err, "get hostname")
	}

	namespace, err := k8s.Namespace()
	if err != nil {
		return "", errors.Wrap(err, "get namespace")
	}

	return fmt.Sprintf("%s.%s.%s", hostname, svcName, namespace), nil
}

func getSecret(username string) (string, error) {
	path := filepath.Join(mysql.CredsMountPath, username)
	sBytes, err := ioutil.ReadFile(path)
	if err != nil {
		return "", errors.Wrapf(err, "read %s", path)
	}

	return strings.TrimSpace(string(sBytes)), nil
}

func getPodIP(hostname string) (string, error) {

	addrs, err := net.LookupHost(hostname)
	if err != nil {
		return "", errors.Wrapf(err, "lookup %s", hostname)
	}
	log.Println("lookup", hostname, addrs)

	return addrs[0], nil
}

func lookup(svcName string) (sets.String, error) {
	endpoints := sets.NewString()
	_, srvRecords, err := net.LookupSRV("", "", svcName)
	if err != nil {
		return endpoints, err
	}
	for _, srvRecord := range srvRecords {
		// The SRV records have the pattern $HOSTNAME.$SERVICE.$.NAMESPACE.svc.$CLUSTER_DNS_SUFFIX
		// We only want $HOSTNAME.$SERVICE.$NAMESPACE because in the `selectDonor` function we
		// compare the list generated here with the output of the `getFQDN` function
		srv := strings.Split(srvRecord.Target, ".")
		ep := strings.Join(srv[:3], ".")
		endpoints.Insert(ep)
	}
	return endpoints, nil
}

func getTopology(peers sets.String) (string, []string, error) {
	replicas := sets.NewString()
	primary := ""

	operatorPass, err := getSecret(v2.USERS_SECRET_KEY_OPERATOR)
	if err != nil {
		return "", nil, errors.Wrapf(err, "get %s password", v2.USERS_SECRET_KEY_OPERATOR)
	}

	for _, peer := range peers.List() {
		db, err := mysql.NewReplicator("operator", operatorPass, peer, mysql.DefaultAdminPort)
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

		if status == mysql.ReplicationStatusActive {
			primary = source
		}
	}

	if primary == "" && peers.Len() == 1 {
		primary = peers.List()[0]
	} else if primary == "" {
		primary = replicas.List()[0]
	}

	if replicas.Len() > 0 {
		replicas.Delete(primary)
	}

	return primary, replicas.List(), nil
}

func selectDonor(fqdn, primary string, replicas []string) (string, error) {
	donor := ""

	operatorPass, err := getSecret(v2.USERS_SECRET_KEY_OPERATOR)
	if err != nil {
		return "", errors.Wrapf(err, "get %s password", v2.USERS_SECRET_KEY_OPERATOR)
	}

	for _, replica := range replicas {
		db, err := mysql.NewReplicator("operator", operatorPass, replica, mysql.DefaultAdminPort)
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
