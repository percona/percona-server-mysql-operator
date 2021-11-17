package k8s

import psmdbv2 "github.com/percona/percona-server-mysql-operator/api/v2"

func Namespace(cr *psmdbv2.PerconaServerForMySQL) string {
	return cr.Namespace
}

func SecretsName(cr *psmdbv2.PerconaServerForMySQL) string {
	return cr.Spec.SecretsName
}

func SSLSecretName(cr *psmdbv2.PerconaServerForMySQL) string {
	return cr.Spec.SSLSecretName
}
