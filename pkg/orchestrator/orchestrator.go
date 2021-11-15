package orchestrator

import (
	v2 "github.com/percona/percona-server-mysql-operator/api/v2"
)

const (
	componentName   = "orc"
	defaultWebPort  = 3000
	defaultRaftPort = 10008
	dataVolumeName  = "datadir"
	DataMountPath   = "/var/lib/orchestrator"
	// configVolumeName = "config"
	// configMountPath  = "/etc/orchestrator"
	credsVolumeName = "users"
	CredsMountPath  = "/etc/orchestrator/orchestrator-users-secret"
	tlsVolumeName   = "tls"
	tlsMountPath    = "/etc/orchestrator/ssl"
)

func name(cr *v2.PerconaServerForMySQL) string {
	return cr.Name + "-" + componentName
}

func namespace(cr *v2.PerconaServerForMySQL) string {
	return cr.Namespace
}
