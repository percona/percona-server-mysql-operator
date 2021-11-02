package orchestrator

import v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"

func (o *Orchestrator) MatchLabels() map[string]string {
	labels := o.cluster.Labels()

	labels[v2.ComponentLabel] = ComponentName

	for k, v := range o.Labels {
		if _, ok := labels[k]; !ok {
			labels[k] = v
		}
	}

	return labels
}
