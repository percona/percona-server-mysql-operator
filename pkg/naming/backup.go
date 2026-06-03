package naming

func EncryptionKeyInternalSecretName(clusterName string) string {
	return "internal-encryption-keys-" + clusterName
}

func InternalEncryptionKeyFileName(clusterName, storageName string) string {
	name := clusterName
	if storageName != "" {
		name = name + "-" + storageName
	}
	return name
}
