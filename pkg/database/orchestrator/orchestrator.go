package orchestrator

import (
	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
)

const (
	ComponentName    = "orc"
	DefaultWebPort   = 3000
	DefaultRaftPort  = 10008
	DataVolumeName   = "datadir"
	DataMountPath    = "/var/lib/orchestrator"
	ConfigVolumeName = "config"
	ConfigMountPath  = "/etc/orchestrator"
	CredsVolumeName  = "users"
	CredsMountPath   = "/etc/orchestrator/orchestrator-users-secret"
	TLSVolumeName    = "tls"
	TLSMountPath     = "/etc/orchestrator/ssl"
)

type Orchestrator struct {
	v2.PodSpec

	cluster *v2.PerconaServerForMySQL
}

func New(cr *v2.PerconaServerForMySQL) *Orchestrator {
	return &Orchestrator{
		PodSpec: cr.Spec.Orchestrator,
		cluster: cr,
	}
}

func (o *Orchestrator) Name() string {
	return o.cluster.Name + "-" + ComponentName
}

func (o *Orchestrator) Namespace() string {
	return o.cluster.Namespace
}
