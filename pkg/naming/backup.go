package naming

func EncryptionKeyInternalSecretName(clusterName string) string {
	return "internal-encryption-keys-" + clusterName
}
