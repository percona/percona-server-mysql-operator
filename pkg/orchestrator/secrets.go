package orchestrator

import v2 "github.com/percona/percona-server-mysql-operator/api/v2"

func secretsName(cr *v2.PerconaServerForMySQL) string {
	return cr.Spec.SecretsName
}

func sslSecretsName(cr *v2.PerconaServerForMySQL) string {
	return cr.Spec.SSLSecretName
}
