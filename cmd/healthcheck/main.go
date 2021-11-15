package main

import (
	"io/ioutil"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/database/mysql"
)

func main() {
	switch os.Args[1] {
	case "readiness":
		if err := checkReadiness(); err != nil {
			log.Fatalf("readiness check failed: %v", err)
		}
	case "liveness":
		if err := checkLiveness(); err != nil {
			log.Fatalf("liveness check failed: %v", err)
		}
	default:
		log.Fatalf("Usage: %s liveness|readiness", os.Args[0])
	}
}

func checkReadiness() error {
	podIP, err := getPodIP()
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}

	operatorPass, err := getSecret(v2.USERS_SECRET_KEY_OPERATOR)
	if err != nil {
		return errors.Wrapf(err, "get %s password", v2.USERS_SECRET_KEY_OPERATOR)
	}

	db, err := mysql.NewReplicator("operator", operatorPass, podIP, mysql.DefaultAdminPort)
	if err != nil {
		return errors.Wrap(err, "connect to db")
	}
	defer db.Close()

	readOnly, err := db.IsReadonly()
	if err != nil {
		return errors.Wrap(err, "check read only status")
	}

	// if isReplica is true, replication is active
	isReplica, err := db.IsReplica()
	if err != nil {
		return errors.Wrap(err, "check replica status")
	}

	if isReplica && !readOnly {
		return errors.New("replica is not read only")
	}

	return nil
}

func checkLiveness() error {
	podIP, err := getPodIP()
	if err != nil {
		return errors.Wrap(err, "get pod IP")
	}

	operatorPass, err := getSecret(v2.USERS_SECRET_KEY_OPERATOR)
	if err != nil {
		return errors.Wrapf(err, "get %s password", v2.USERS_SECRET_KEY_OPERATOR)
	}

	db, err := mysql.NewReplicator("operator", operatorPass, podIP, mysql.DefaultAdminPort)
	if err != nil {
		return errors.Wrap(err, "connect to db")
	}
	defer db.Close()

	return db.DumbQuery()
}

func getSecret(username string) (string, error) {
	path := filepath.Join(mysql.CredsMountPath, username)
	sBytes, err := ioutil.ReadFile(path)
	if err != nil {
		return "", errors.Wrapf(err, "read %s", path)
	}

	return strings.TrimSpace(string(sBytes)), nil
}

func getPodIP() (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", errors.Wrap(err, "get hostname")
	}

	addrs, err := net.LookupHost(hostname)
	if err != nil {
		return "", errors.Wrapf(err, "lookup %s", hostname)
	}

	return addrs[0], nil
}
