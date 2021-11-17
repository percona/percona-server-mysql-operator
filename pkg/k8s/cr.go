package k8s

import apiv2 "github.com/percona/percona-server-mysql-operator/api/v2"

func Namespace(cr *apiv2.PerconaServerForMySQL) string {
	return cr.Namespace
}

func SecretsName(cr *apiv2.PerconaServerForMySQL) string {
	return cr.Spec.SecretsName
}

func SSLSecretName(cr *apiv2.PerconaServerForMySQL) string {
	return cr.Spec.SSLSecretName
}
