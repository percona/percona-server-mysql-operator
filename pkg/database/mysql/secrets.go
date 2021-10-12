package mysql

func (m *MySQL) SecretsName() string {
	return m.cluster.Spec.SecretsName
}

func (m *MySQL) SSLSecretsName() string {
	return m.cluster.Spec.SSLSecretName
}
