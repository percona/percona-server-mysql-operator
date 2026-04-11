package naming

func RestoreLeaseName(clusterName string) string {
	return "restore-lock-" + clusterName
}
