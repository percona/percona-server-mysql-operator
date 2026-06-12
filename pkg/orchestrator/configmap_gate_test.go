package orchestrator

import (
	"strings"
	"testing"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestConfigMapDataVersionGate(t *testing.T) {
	cr := &apiv1.PerconaServerMySQL{}
	cr.Spec.CRVersion = "1.1.0"
	cr.Spec.SSLSecretName = "ssl"
	data, err := ConfigMapData(cr)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(data, "MySQLTopologySSL") {
		t.Fatalf("1.1.0 CR must not get SSL keys: %s", data)
	}

	cr.Spec.CRVersion = "1.2.0"
	data, err = ConfigMapData(cr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "MySQLTopologySSLCAFile") {
		t.Fatalf("1.2.0 CR must get SSL keys: %s", data)
	}
}
