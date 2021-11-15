package util

// copySSMap makes (shallow) copy of src map
func CopySSMap(src map[string]string) map[string]string {
	dst := make(map[string]string)
	for k, v := range src {
		dst[k] = v
	}

	return dst
}

// mergeSSMap merges maps ms from left to right. Keys values are overwritten
func MergeSSMap(ms ...map[string]string) map[string]string {
	if len(ms) == 0 {
		return make(map[string]string)
	}

	rv := CopySSMap(ms[0])
	for _, m := range ms[1:] {
		for k, v := range m {
			rv[k] = v
		}
	}

	return rv
}
