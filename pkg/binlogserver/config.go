package binlogserver

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
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

type Configurable apiv1.PerconaServerMySQL

func (c *Configurable) GetConfigMapName() string {
	cr := apiv1.PerconaServerMySQL(*c)
	return customConfigMapName(&cr)
}

func (c *Configurable) GetConfigMapKey() string {
	return customConfigKey
}

func (c *Configurable) GetConfiguration() string {
	cr := apiv1.PerconaServerMySQL(*c)
	return cr.Spec.Backup.PiTR.BinlogServer.Configuration
}

func (c *Configurable) GetResources() corev1.ResourceRequirements {
	cr := apiv1.PerconaServerMySQL(*c)
	return cr.Spec.Backup.PiTR.BinlogServer.Resources
}

func (c *Configurable) ExecuteConfigurationTemplate(input string, memory *resource.Quantity) (string, error) {
	return input, nil
}
