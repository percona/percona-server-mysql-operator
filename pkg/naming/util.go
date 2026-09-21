package naming

import (
	"crypto/md5"
	"fmt"
	"strings"
)

// TruncateNameWithHash shortens a name and adds a hash when it exceeds maxLength
func TruncateNameWithHash(name string, maxLength int, hashPrefix string) string {
	if len(name) <= maxLength {
		return name
	}

	hash := fmt.Sprintf("%x", md5.Sum([]byte(name)))[:8]
	suffix := hashPrefix + hash
	prefix := strings.TrimRight(name[:maxLength-len(suffix)], "-")
	return prefix + suffix
}
