package orchestrator

func (o *Orchestrator) SecretsName() string {
	return o.cluster.Spec.SecretsName
}

func (o *Orchestrator) SSLSecretsName() string {
	return o.cluster.Spec.SSLSecretName
}
