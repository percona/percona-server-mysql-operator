package mysql

func (m *MySQL) MatchLabels() map[string]string {
	labels := m.clusterLabels
	for k, v := range m.Labels {
		if _, ok := labels[k]; !ok {
			labels[k] = v
		}
	}

	return labels
}
