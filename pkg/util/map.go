package util

import "maps"

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

// SSMapMerge merges maps ms from left to right with overwriting existing keys
func SSMapMerge(ms ...map[string]string) map[string]string {
	if len(ms) == 0 {
		return make(map[string]string)
	}

	rv := make(map[string]string)
	for _, m := range ms {
		maps.Copy(rv, m)
	}

	return rv
}

// SSMapFilterByKeys returns a new map that contains keys from the keys array existing in the m map.
func SSMapFilterByKeys(m map[string]string, keys []string) map[string]string {
	if len(m) == 0 || len(keys) == 0 {
		return nil
	}
	filteredMap := make(map[string]string)
	for _, k := range keys {
		if v, ok := m[k]; ok {
			filteredMap[k] = v
		}
	}
	return filteredMap
}
