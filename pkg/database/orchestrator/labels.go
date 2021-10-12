package orchestrator

func (o *Orchestrator) MatchLabels() map[string]string {
	labels := o.cluster.Labels()

	labels["app.kubernetes.io/component"] = Name

	for k, v := range o.Labels {
		if _, ok := labels[k]; !ok {
			labels[k] = v
		}
	}

	return labels
}
