package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

// getFQDN generates the Fully Qualified Domain Name from the service name.
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

// getSecret retrieves the secret associated with a given username.
func getSecret(username apiv1alpha1.SystemUser) (string, error) {
	path := filepath.Join(mysql.CredsMountPath, string(username))
	sBytes, err := os.ReadFile(path)
	if err != nil {
		return "", errors.Wrapf(err, "read %s", path)
	}

	return strings.TrimSpace(string(sBytes)), nil
}

// getPodIP fetches the IP address of a given hostname.
func getPodIP(hostname string) (string, error) {
	addrs, err := net.LookupHost(hostname)
	if err != nil {
		return "", errors.Wrapf(err, "lookup %s", hostname)
	}
	log.Println("lookup", hostname, addrs)

	return addrs[0], nil
}

// lookup resolves a service name to its SRV records, extracting relevant endpoints.
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

// lockExists checks if the bootstrap.lock file exists.
func lockExists() (bool, error) {
	return fileExists("/var/lib/mysql/bootstrap.lock")
}

// fileExists determines if a specified file exists.
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

// waitLockRemoval repeatedly checks for the removal of the bootstrap.lock file.
func waitLockRemoval() error {
	for {
		exists, err := lockExists()
		if err != nil {
			return err
		}
		time.Sleep(time.Second)
		if !exists {
			return nil
		}
	}
}

// createFile creates a new file with the specified content.
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
