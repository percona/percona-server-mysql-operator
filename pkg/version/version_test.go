package version_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"

	"github.com/percona/percona-server-mysql-operator/pkg/naming"
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
	yamlDocs := bytes.Split(data, []byte("\n---\n"))
	for _, doc := range yamlDocs {
		if len(doc) == 0 {
			continue
		}
		crd := new(v1.CustomResourceDefinition)
		if err := yaml.Unmarshal(doc, crd); err != nil {
			t.Fatalf("Failed to unmarshal crd: %s", err.Error())
		}
		expectedLabels := naming.Labels("percona-server-crd", "", "percona-server-mysql-operator", "crd")
		delete(expectedLabels, naming.LabelManagedBy)

		for k, expectedValue := range expectedLabels {
			if crd.Labels[k] == expectedValue {
				continue
			}
			t.Logf("invalid value is specified in %s label of %s CustomResourceDefinition: have: %s, expected: %s", k, crd.Name, crd.Labels[k], expectedValue)
			t.Log([]byte(crd.Labels[k]), []byte(expectedValue))
			t.Fail()
		}
	}
}
