package xtrabackup

import (
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func readDefaultCluster(t *testing.T, name, namespace string) *apiv1.PerconaServerMySQL {
	t.Helper()

	cr := &apiv1.PerconaServerMySQL{}
	readDefaultFile(t, "cr.yaml", cr)

	cr.Name = name
	cr.Namespace = namespace
	cr.Spec.InitContainer.Image = "init-image"
	return cr
}

func readDefaultBackup(t *testing.T, name, namespace string) *apiv1.PerconaServerMySQLBackup {
	t.Helper()

	cr := &apiv1.PerconaServerMySQLBackup{}
	readDefaultFile(t, "backup.yaml", cr)

	cr.Status.Storage = new(apiv1.BackupStorageSpec)
	cr.Name = name
	cr.Namespace = namespace
	return cr
}

func readDefaultFile[T any](t *testing.T, filename string, obj *T) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", filename))
	if err != nil {
		t.Fatal(err)
	}

	if err := yaml.Unmarshal(data, obj); err != nil {
		t.Fatal(err)
	}
}
