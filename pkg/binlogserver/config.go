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

type Connection struct {
	Host           string `json:"host,omitempty"`
	Port           int32  `json:"port,omitempty"`
	User           string `json:"user,omitempty"`
	Password       string `json:"password,omitempty"`
	ConnectTimeout int32  `json:"connect_timeout,omitempty"`
	ReadTimeout    int32  `json:"read_timeout,omitempty"`
	WriteTimeout   int32  `json:"write_timeout,omitempty"`
}

type Replication struct {
	ServerID int32 `json:"server_id,omitempty"`
	IdleTime int32 `json:"idle_time,omitempty"`
}

type Storage struct {
	URI string `json:"uri,omitempty"`
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
	return Name(&cr)
}

func (c *Configurable) GetConfigMapKey() string {
	return CustomConfigKey
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
