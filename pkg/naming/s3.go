package naming

const (
	S3CertsInputVolumeName = "s3-certs-in"
	S3CertsInputMountPath  = "/etc/s3/certs-in"
	S3CertsVolumeName      = "s3-certs"
	S3CertsMountPath       = "/etc/s3/certs"
	S3CABundlePath         = S3CertsMountPath + "/ca-bundle.crt"
)
