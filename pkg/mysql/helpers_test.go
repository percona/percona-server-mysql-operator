package mysql

import (
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

func readDefaultCluster(t *testing.T, name, namespace string) *apiv1alpha1.PerconaServerMySQL {
	t.Helper()

	cr := &apiv1alpha1.PerconaServerMySQL{}
	readDefaultFile(t, "cr.yaml", cr)

	cr.Name = name
	cr.Namespace = namespace
	cr.Spec.InitContainer.Image = "init-image"
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
