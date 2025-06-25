package router

import (
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"
)

func TestPorts(t *testing.T) {
	defaultPorts := func() []corev1.ServicePort {
		return []corev1.ServicePort{
			{
				Name: "http",
				Port: 8443,
			},
			{
				Name: "rw-default",
				Port: 3306,
				TargetPort: intstr.IntOrString{
					IntVal: 6446,
				},
			},
			{
				Name: "read-write",
				Port: 6446,
			},
			{
				Name: "read-only",
				Port: 6447,
			},
			{
				Name: "x-read-write",
				Port: 6448,
			},
			{
				Name: "x-read-only",
				Port: 6449,
			},
			{
				Name: "x-default",
				Port: 33060,
			},
			{
				Name: "rw-admin",
				Port: 33062,
			},
		}
	}

	tests := []struct {
		name           string
		specifiedPorts []corev1.ServicePort
		expectedPorts  []corev1.ServicePort
	}{
		{
			name:          "default ports",
			expectedPorts: defaultPorts(),
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
			expectedPorts: updateObject(defaultPorts(), func(ports []corev1.ServicePort) []corev1.ServicePort {
				ports = append(ports, []corev1.ServicePort{
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
				}...)
				return ports
			}),
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
			expectedPorts: updateObject(defaultPorts(), func(ports []corev1.ServicePort) []corev1.ServicePort {
				for i, v := range ports {
					if v.Name == "http" {
						ports[i].Port = 5555
						continue
					}
					if v.Name == "rw-default" {
						ports[i].Port = 6666
						ports[i].TargetPort = intstr.IntOrString{
							Type:   intstr.Int,
							IntVal: 30,
						}
						continue
					}
				}
				ports = append(ports, []corev1.ServicePort{
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
				}...)
				return ports
			}),
		},
		{
			name: "modified port with default targetPort",
			specifiedPorts: []corev1.ServicePort{
				{
					Name: "rw-default",
					Port: 6666,
				},
			},
			expectedPorts: updateObject(defaultPorts(), func(ports []corev1.ServicePort) []corev1.ServicePort {
				for i, v := range ports {
					if v.Name == "rw-default" {
						ports[i].Port = 6666
						break
					}
				}
				return ports
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ports(tt.specifiedPorts)
			if !reflect.DeepEqual(got, tt.expectedPorts) {
				gotBytes, err := yaml.Marshal(got)
				if err != nil {
					t.Fatalf("error marshaling got: %v", err)
				}
				wantBytes, err := yaml.Marshal(tt.expectedPorts)
				if err != nil {
					t.Fatalf("error marshaling want: %v", err)
				}
				t.Fatal(cmp.Diff(string(wantBytes), string(gotBytes)))
			}
		})
	}
}

func updateObject[T any](obj T, updateFuncs ...func(obj T) T) T {
	for _, f := range updateFuncs {
		obj = f(obj)
	}
	return obj
}
