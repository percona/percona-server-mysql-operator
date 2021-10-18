package mysql

import (
	"fmt"

	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	corev1 "k8s.io/api/core/v1"
)

func (m *MySQL) ConfigMapName() string {
	return m.Name()
}

func (m *MySQL) Configuration() map[string]string {
	return map[string]string{
		"ssl_ca":                   TLSMountPath + "/ca.crt",
		"ssl_cert":                 TLSMountPath + "/tls.crt",
		"ssl_key":                  TLSMountPath + "/tls.key",
		"enforce-gtid-consistency": "",
		"gtid_mode":                "ON",
		"server_id":                "0",
		"report_host":              "FQDN",
		"report_port":              "3306",
		"admin-address":            "IP",
		"plugin-load-add":          "clone=mysql_clone.so",
	}
}

func (m *MySQL) ConfigMap(cfg map[string]string) *corev1.ConfigMap {
	mysqld := "[mysqld]\n"

	for k, v := range cfg {
		if v != "" {
			mysqld += fmt.Sprintf("%s = %s\n", k, v)
		} else {
			mysqld += fmt.Sprintf("%s\n", k)
		}
	}

	data := map[string]string{
		"node.cnf": mysqld,
	}

	return k8s.ConfigMap(m.Namespace(), m.ConfigMapName(), data)
}
