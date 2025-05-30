package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

func TestStatefulset(t *testing.T) {
	const ns = "orc-ns"

	cr := readDefaultCluster(t, "cluster", ns)

	tests := []struct {
		name string

		cluster    *apiv1alpha1.PerconaServerMySQL
		configHash string
		tlsHash    string

		compareFile string
	}{
		{
			name:        "default cr",
			cluster:     cr.DeepCopy(),
			configHash:  "config-hash",
			tlsHash:     "tls-hash",
			compareFile: "default-sts.yaml",
		},
		{
			name: "default cr with image pull secrets",
			cluster: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQL) {
				cr.Spec.Orchestrator.ImagePullSecrets = []corev1.LocalObjectReference{
					{
						Name: "secret-1",
					},
					{
						Name: "secret-2",
					},
				}
			}),
			configHash:  "config-hash",
			tlsHash:     "tls-hash",
			compareFile: "image-pull-secrets-sts.yaml",
		},
		{
			name: "default cr with runtime class name",
			cluster: updateResource(cr.DeepCopy(), func(cr *apiv1alpha1.PerconaServerMySQL) {
				n := "runtime-class-name"
				cr.Spec.Orchestrator.RuntimeClassName = &n
			}),
			configHash:  "config-hash",
			tlsHash:     "tls-hash",
			compareFile: "runtime-class-name-sts.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := StatefulSet(tt.cluster, tt.configHash, tt.tlsHash)
			compareObj(t, s, expectedObject[*appsv1.StatefulSet](t, tt.compareFile))
		})
	}
}

func expectedObject[T client.Object](t *testing.T, path string) T {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", path))
	if err != nil {
		t.Fatal(err)
	}

	obj := new(T)
	if err := yaml.Unmarshal(data, obj); err != nil {
		t.Fatal(err)
	}

	return *obj
}

func compareObj[T client.Object](t *testing.T, got, want T) {
	t.Helper()

	gotBytes, err := yaml.Marshal(got)
	if err != nil {
		t.Fatalf("error marshaling got: %v", err)
	}
	wantBytes, err := yaml.Marshal(want)
	if err != nil {
		t.Fatalf("error marshaling want: %v", err)
	}
	if string(gotBytes) != string(wantBytes) {
		t.Fatal(cmp.Diff(string(wantBytes), string(gotBytes)))
	}
}
