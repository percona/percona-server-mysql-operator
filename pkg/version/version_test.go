package version_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
)

func TestCRDVersionLabel(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("Failed to get caller information")
	}
	dir := filepath.Dir(filename)
	crdPath := filepath.Join(dir, "..", "..", "deploy", "crd.yaml")

	data, err := os.ReadFile(crdPath)
	if err != nil {
		t.Fatalf("Failed to read file: %s", err.Error())
	}
	crd := new(v1.CustomResourceDefinition)
	if err := yaml.Unmarshal(data, crd); err != nil {
		t.Fatalf("Failed to unmarshal crd: %s", err.Error())
	}

	expected := "v" + version.Version
	if crd.Labels[naming.LabelOperatorVersion] != expected {
		t.Fatalf("invalid version is specified in %s label of crd.yaml: expected: %s", naming.LabelOperatorVersion, expected)
	}
}
