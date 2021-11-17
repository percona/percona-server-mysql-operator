package util

// SSMapEqual compares maps for equality
func SSMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if b[k] != v {
			return false
		}
	}

	return true
}

// SSMapCopy makes (shallow) copy of src map
func SSMapCopy(src map[string]string) map[string]string {
	dst := make(map[string]string)
	for k, v := range src {
		dst[k] = v
	}

	return dst
}

// SSMapMerge merges maps ms from left to right with overwriting existing keys
func SSMapMerge(ms ...map[string]string) map[string]string {
	if len(ms) == 0 {
		return make(map[string]string)
	}

	rv := SSMapCopy(ms[0])
	for _, m := range ms[1:] {
		for k, v := range m {
			rv[k] = v
		}
	}

	return rv
}
