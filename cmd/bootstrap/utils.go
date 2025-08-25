package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	state "github.com/percona/percona-server-mysql-operator/cmd/internal/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func getFQDN(svcName string) (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", errors.Wrap(err, "get hostname")
	}

	namespace, err := k8s.DefaultAPINamespace()
	if err != nil {
		return "", errors.Wrap(err, "get namespace")
	}

	return fmt.Sprintf("%s.%s.%s", hostname, svcName, namespace), nil
}

func getReadTimeout() (uint32, error) {
	s, ok := os.LookupEnv(naming.EnvBootstrapReadTimeout)
	if !ok {
		return 0, nil
	}
	readTimeout, err := strconv.Atoi(s)
	if err != nil {
		return 0, errors.Wrap(err, "failed to parse BOOTSTRAP_READ_TIMEOUT")
	}
	if readTimeout < 0 {
		return 0, errors.New("BOOTSTRAP_READ_TIMEOUT should be a positive value")
	}

	return uint32(readTimeout), nil
}

func getSecret(username apiv1.SystemUser) (string, error) {
	path := filepath.Join(mysql.CredsMountPath, string(username))
	sBytes, err := os.ReadFile(path)
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

func lookup(svcName string) (sets.Set[string], error) {
	endpoints := sets.New[string]()
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

func lockExists(lockName string) (bool, error) {
	return fileExists(fmt.Sprintf("/var/lib/mysql/%s.lock", lockName))
}

func fileExists(name string) (bool, error) {
	_, err := os.Stat(name)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, errors.Wrap(err, "os stat")
	}
	return true, nil
}

func waitLockRemoval(lockName string) error {
	for {
		exists, err := lockExists(lockName)
		if err != nil {
			return err
		}
		time.Sleep(time.Second)
		if !exists {
			return nil
		}
	}
}

func waitForMySQLReadyState() error {
	stateFilePath, ok := os.LookupEnv(naming.EnvMySQLStateFile)
	if !ok {
		return errors.New("env var MYSQL_STATE_FILE is required")
	}

	for {
		mysqlState, err := os.ReadFile(stateFilePath)
		if err != nil {
			return errors.Wrap(err, "read mysql state")
		}
		if string(mysqlState) == string(state.MySQLReady) {
			return nil
		}
		time.Sleep(time.Second)
	}
}

func createFile(name, content string) error {
	f, err := os.Create(name)
	if err != nil {
		return errors.Wrapf(err, "create %s", name)
	}

	_, err = f.WriteString(content)
	if err != nil {
		return errors.Wrapf(err, "write to %s", name)
	}

	return nil
}
