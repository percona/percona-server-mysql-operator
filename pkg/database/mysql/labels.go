package mysql

import v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"

func (m *MySQL) MatchLabels() map[string]string {
	labels := m.cluster.Labels()

	labels[v2.ComponentLabel] = ComponentName

	for k, v := range m.Labels {
		if _, ok := labels[k]; !ok {
			labels[k] = v
		}
	}

	return labels
}
