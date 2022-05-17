package util

// Difference returns items that exist in b but not in a
func Difference(a, b []string) (diff []string) {
	m := make(map[string]struct{})

	for _, item := range b {
		m[item] = struct{}{}
	}

	for _, item := range a {
		if _, ok := m[item]; !ok {
			diff = append(diff, item)
		}
	}

	return
}
