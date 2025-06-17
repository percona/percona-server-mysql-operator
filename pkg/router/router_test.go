package router

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"
)

func TestPorts(t *testing.T) {
	tests := []struct {
		name              string
		specifiedPorts    []corev1.ServicePort
		expectedPortsFile string
	}{
		{
			name:              "default ports",
			expectedPortsFile: "default-ports.yaml",
		},
		{
			name: "additional ports",
			specifiedPorts: []corev1.ServicePort{
				{
					Name: "additional port",
					Port: 4308,
				},
				{
					Name: "additional port with target port",
					Port: 1337,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 20,
					},
				},
			},
			expectedPortsFile: "add-ports.yaml",
		},
		{
			name: "modified ports with additional ports",
			specifiedPorts: []corev1.ServicePort{
				{
					Name: "http",
					Port: 5555,
				},
				{
					Name: "rw-default",
					Port: 6666,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 30,
					},
				},
				{
					Name: "additional port",
					Port: 4308,
				},
				{
					Name: "additional port with target port",
					Port: 1337,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 20,
					},
				},
			},
			expectedPortsFile: "mod-add-ports.yaml",
		},
		{
			name: "modified port with default targetPort",
			specifiedPorts: []corev1.ServicePort{
				{
					Name: "rw-default",
					Port: 6666,
				},
			},
			expectedPortsFile: "mod-def-targetport-ports.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ports(tt.specifiedPorts)
			data, err := yaml.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			err = os.WriteFile(filepath.Join("testdata", "ports", tt.expectedPortsFile), data, 0666)
			if err != nil {
				t.Fatal(err)
			}
			expected := expectedObject[[]corev1.ServicePort](t, filepath.Join("ports", tt.expectedPortsFile))
			compareObj(t, got, expected)
		})
	}
}

func expectedObject[T any](t *testing.T, path string) T {
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

func compareObj[T any](t *testing.T, got, want T) {
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
