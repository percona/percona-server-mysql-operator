package orchestrator

import (
	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
)

const (
	Name             = "orchestrator"
	DataVolumeName   = "datadir"
	DataMountPath    = "/var/lib/orchestrator"
	ConfigVolumeName = "config"
	ConfigMountPath  = "/etc/orchestrator"
	CredsVolumeName  = "credentials"
	CredsMountPath   = "/etc/orchestrator/credentials"
	TLSVolumeName    = "tls"
	TLSMountPath     = "/etc/orchestrator/ssl"
)

type Orchestrator struct {
	v2.PodSpec

	Name          string
	Namespace     string
	secretsName   string
	sslSecretName string
	clusterLabels map[string]string
}

func New(cr *v2.PerconaServerForMySQL) *Orchestrator {
	return &Orchestrator{
		PodSpec:       cr.Spec.Orchestrator,
		Name:          cr.Name + "-" + Name,
		Namespace:     cr.Namespace,
		secretsName:   cr.Spec.SecretsName,
		sslSecretName: cr.Spec.SSLSecretName,
		clusterLabels: cr.Labels(),
	}
}
