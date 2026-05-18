package binlogserver

import (
	"context"
	"fmt"
	"net/url"
	"path"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/secret"
)

type Logger struct {
	Level string `json:"level,omitempty"`
	File  string `json:"file,omitempty"`
}

type ConnectionSSL struct {
	Mode    string `json:"mode,omitempty"`
	CA      string `json:"ca,omitempty"`
	CAPath  string `json:"capath,omitempty"`
	CRL     string `json:"crl,omitempty"`
	CRLPath string `json:"crlpath,omitempty"`
	Cert    string `json:"cert,omitempty"`
	Key     string `json:"key,omitempty"`
	Cipher  string `json:"cipher,omitempty"`
}

type ConnectionTLS struct {
	CipherSuites string `json:"ciphersuites,omitempty"`
	Version      string `json:"version,omitempty"`
}

type Connection struct {
	Host           string         `json:"host,omitempty"`
	Port           int32          `json:"port,omitempty"`
	User           string         `json:"user,omitempty"`
	Password       string         `json:"password,omitempty"`
	ConnectTimeout int32          `json:"connect_timeout,omitempty"`
	ReadTimeout    int32          `json:"read_timeout,omitempty"`
	WriteTimeout   int32          `json:"write_timeout,omitempty"`
	SSL            *ConnectionSSL `json:"ssl,omitempty"`
	TLS            *ConnectionTLS `json:"tls,omitempty"`
}

type ReplicationMode string

const (
	ReplicationModeGTID ReplicationMode = "gtid"
)

type Rewrite struct {
	BaseFileName string `json:"base_file_name,omitempty"`
	FileSize     string `json:"file_size,omitempty"`
}

type Replication struct {
	ServerID       int32           `json:"server_id,omitempty"`
	IdleTime       int32           `json:"idle_time,omitempty"`
	VerifyChecksum bool            `json:"verify_checksum,omitempty"`
	Mode           ReplicationMode `json:"mode,omitempty"`
	Rewrite        Rewrite         `json:"rewrite,omitempty"`
}

type Storage struct {
	Backend            string `json:"backend,omitempty"`
	URI                string `json:"uri,omitempty"`
	FsBufferDirectory  string `json:"fs_buffer_directory,omitempty"`
	CheckpointSize     string `json:"checkpoint_size,omitempty"`
	CheckpointInterval string `json:"checkpoint_interval,omitempty"`
}

type Configuration struct {
	Logger      Logger      `json:"logger,omitempty"`
	Connection  Connection  `json:"connection"`
	Replication Replication `json:"replication,omitempty"`
	Storage     Storage     `json:"storage,omitempty"`
}

var ErrNoCredentials = errors.New("no binlog server credentials")

func GetConfiguration(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQL, spec *apiv1.BinlogServerSpec) (Configuration, error) {
	s3 := spec.Storage.S3

	if s3 == nil || len(s3.CredentialsSecret) == 0 {
		return Configuration{}, ErrNoCredentials
	}

	s3Secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.Storage.S3.CredentialsSecret,
			Namespace: cr.Namespace,
		},
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(&s3Secret), &s3Secret); err != nil {
		return Configuration{}, errors.Wrap(err, "get s3 credentials secret")
	}

	accessKey := s3Secret.Data[secret.CredentialsAWSAccessKey]
	secretKey := s3Secret.Data[secret.CredentialsAWSSecretKey]

	s3Uri, err := s3URI(*s3, accessKey, secretKey)
	if err != nil {
		return Configuration{}, errors.Wrap(err, "get s3 uri")
	}

	replPass, err := k8s.UserPassword(ctx, cl, cr, apiv1.UserReplication)
	if err != nil {
		return Configuration{}, errors.Wrap(err, "get replication password")
	}
	verifyChecksum := true
	if spec.VerifyChecksum != nil {
		verifyChecksum = *spec.VerifyChecksum
	}

	return Configuration{
		Logger: Logger{
			Level: spec.LogLevel,
			File:  "/dev/stdout",
		},
		Connection: Connection{
			Host:           fmt.Sprintf("%s.%s", mysql.PrimaryServiceName(cr), cr.Namespace),
			Port:           3306,
			User:           string(apiv1.UserReplication),
			Password:       replPass,
			ConnectTimeout: spec.ConnectTimeout,
			WriteTimeout:   spec.WriteTimeout,
			ReadTimeout:    spec.ReadTimeout,
			SSL:            binlogServerSSLConfig(spec.SSLMode),
		},
		Replication: Replication{
			Mode:           ReplicationModeGTID,
			ServerID:       spec.ServerID,
			IdleTime:       spec.IdleTime,
			VerifyChecksum: verifyChecksum,
			Rewrite: Rewrite{
				BaseFileName: "binlog",
				FileSize:     spec.RewriteFileSize,
			},
		},
		Storage: Storage{
			Backend:            "s3",
			URI:                s3Uri,
			CheckpointSize:     spec.CheckpointSize,
			CheckpointInterval: spec.CheckpointInterval,
			FsBufferDirectory:  BufferMountPath,
		},
	}, nil
}

func s3URI(s3 apiv1.BackupStorageS3Spec, accessKey, secretKey []byte) (string, error) {
	bucket := string(s3.Bucket)
	if len(s3.Region) > 0 {
		bucket = fmt.Sprintf("%s.%s", s3.Bucket, s3.Region)
	}
	encodedAccessKey := url.QueryEscape(string(accessKey))
	encodedSecretKey := url.QueryEscape(string(secretKey))
	uri := fmt.Sprintf("s3://%s:%s@%s", encodedAccessKey, encodedSecretKey, bucket)
	if len(s3.EndpointURL) != 0 {
		protocol, host, err := parseEndpointURL(s3.EndpointURL)
		if err != nil {
			return "", errors.Wrap(err, "parse endpoint URL")
		}
		uri = fmt.Sprintf("%s://%s:%s@%s/%s", protocol, encodedAccessKey, encodedSecretKey, host, s3.Bucket)
	}
	if len(s3.Prefix) > 0 {
		uri += fmt.Sprintf("/%s", s3.Prefix)
	}

	return uri, nil
}

// parseEndpointURL extracts the protocol and host from an endpoint URL.
// Expected formats: "s3://s3.amazonaws.com", "https://minio-service:9000"
func parseEndpointURL(endpointURL string) (protocol, host string, err error) {
	u, err := url.Parse(endpointURL)
	if err != nil {
		return "", "", fmt.Errorf("parse endpoint URL %q: %w", endpointURL, err)
	}
	if u.Host == "" {
		return "", "", fmt.Errorf("endpoint URL %q must include protocol and host (e.g. s3://... or https://...)", endpointURL)
	}
	return u.Scheme, u.Host, nil
}

func binlogServerSSLConfig(sslMode string) *ConnectionSSL {
	ssl := &ConnectionSSL{Mode: sslMode}
	if sslMode != "disabled" {
		ssl.CA = path.Join(TLSMountPath, "ca.crt")
		ssl.Cert = path.Join(TLSMountPath, "tls.crt")
		ssl.Key = path.Join(TLSMountPath, "tls.key")
	}
	return ssl
}

type Configurable struct {
	cr   *apiv1.PerconaServerMySQL
	spec *apiv1.BinlogServerSpec
}

func (c *Configurable) GetConfigMapName() string {
	return customConfigMapName(c.cr)
}

func (c *Configurable) GetConfigMapKey() string {
	return customConfigKey
}

func (c *Configurable) GetConfiguration() string {
	return c.spec.Configuration
}

func (c *Configurable) GetResources() corev1.ResourceRequirements {
	return c.spec.Resources
}

func (c *Configurable) ExecuteConfigurationTemplate(input string, memory *resource.Quantity) (string, error) {
	return input, nil
}
