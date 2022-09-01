package mysql

import (
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
)

func GetTopology(hosts []string, operatorPass string) (string, []string, error) {
	replicas := sets.NewString()
	primary := ""

	for _, host := range hosts {
		p, err := getPrimary(host, operatorPass)
		if err != nil {
			return "", nil, errors.Wrap(err, "get primary")
		}
		if p != "" {
			primary = p
		}
		replicas.Insert(host)
	}
	if primary == "" && len(hosts) == 1 {
		primary = hosts[0]
	} else if primary == "" {
		primary = replicas.List()[0]
	}
	if replicas.Len() > 0 {
		replicas.Delete(primary)
	}
	return primary, replicas.List(), nil
}

func getPrimary(host, operatorPass string) (string, error) {
	db, err := replicator.NewReplicator(apiv1alpha1.UserOperator, operatorPass, host, DefaultAdminPort)
	if err != nil {
		return "", errors.Wrapf(err, "connect to %s", host)
	}
	defer db.Close()

	status, source, err := db.ReplicationStatus()
	if err != nil {
		return "", errors.Wrap(err, "check replication status")
	}
	primary := ""
	if status == replicator.ReplicationStatusActive {
		primary = source
	}
	return primary, nil
}
