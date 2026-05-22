package naming

import (
	"strings"

	"k8s.io/apimachinery/pkg/types"
)

func RestoreLeaseName(clusterName string) string {
	return "restore-lock-" + clusterName
}

func BackupLeaseName(clusterName string) string {
	return "ps-" + clusterName + "-backup-lock"
}

func LeaseHolderName(name, uid string) string {
	return name + "|" + uid
}

func ParseLeaseHolder(holder string) (string, types.UID) {
	parts := strings.Split(holder, "|")
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], types.UID(parts[1])
}
