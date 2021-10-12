package mysql

func (m *MySQL) MatchLabels() map[string]string {
	labels := m.cluster.Labels()

	labels["app.kubernetes.io/component"] = Name

	for k, v := range m.Labels {
		if _, ok := labels[k]; !ok {
			labels[k] = v
		}
	}

	return labels
}
