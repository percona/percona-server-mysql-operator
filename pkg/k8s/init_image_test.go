package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

func TestInitContainer(t *testing.T) {
	componentName := "example-component"
	image := "example-image:latest"
	pullPolicy := corev1.PullAlways
	secCtx := &corev1.SecurityContext{}

	expectedVolumeMounts := []corev1.VolumeMount{
		{
			Name:      "bin",
			MountPath: "/opt/percona",
		},
	}
	expectedCommand := []string{"/opt/percona-server-mysql-operator/ps-init-entrypoint.sh"}
	expectedTerminationMessagePath := "/dev/termination-log"
	expectedTerminationMessagePolicy := corev1.TerminationMessageReadFile

	expectedResources := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("250m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
	}

	tests := map[string]struct {
		cr                      *apiv1alpha1.PerconaServerMySQL
		inputVolumes            []corev1.VolumeMount
		expectedVolumes         []corev1.VolumeMount
		expectedResources       corev1.ResourceRequirements
		expectedSecurityContext corev1.SecurityContext
	}{
		"default volumes": {
			expectedVolumes:         expectedVolumeMounts,
			expectedResources:       expectedResources,
			expectedSecurityContext: *secCtx,
		},
		"additional volumes": {
			inputVolumes: []corev1.VolumeMount{
				{
					Name:      "dataVolumeName",
					MountPath: "dataMountPath",
				},
			},
			expectedVolumes: append(expectedVolumeMounts,
				corev1.VolumeMount{
					Name:      "dataVolumeName",
					MountPath: "dataMountPath",
				}),
			expectedResources: expectedResources,
		},
		"initContainer.resources": {
			cr: &apiv1alpha1.PerconaServerMySQL{
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					InitContainer: apiv1alpha1.InitContainerSpec{
						Image: "initcontainer-image",
						Resources: &corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("256Mi"),
							},
						},
					},
				},
			},
			expectedResources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
			},
			expectedVolumes: expectedVolumeMounts,
		},
		"initContainer.containerSecurityContext": {
			expectedVolumes: expectedVolumeMounts,
			cr: &apiv1alpha1.PerconaServerMySQL{
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					InitContainer: apiv1alpha1.InitContainerSpec{
						ContainerSecurityContext: &corev1.SecurityContext{
							Privileged: ptr.To(true),
						},
					},
				},
			},
			expectedSecurityContext: corev1.SecurityContext{
				Privileged: ptr.To(true),
			},
			expectedResources: expectedResources,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cr := new(apiv1alpha1.PerconaServerMySQL)
			if tt.cr != nil {
				cr = tt.cr
			}
			container := InitContainer(cr, componentName, image, pullPolicy, secCtx, expectedResources, tt.inputVolumes)

			assert.Equal(t, componentName+"-init", container.Name)
			assert.Equal(t, image, container.Image)
			assert.Equal(t, pullPolicy, container.ImagePullPolicy)
			assert.Equal(t, tt.expectedVolumes, container.VolumeMounts)
			assert.Equal(t, expectedCommand, container.Command)
			assert.Equal(t, expectedTerminationMessagePath, container.TerminationMessagePath)
			assert.Equal(t, expectedTerminationMessagePolicy, container.TerminationMessagePolicy)
			assert.Equal(t, tt.expectedSecurityContext, *container.SecurityContext)
			assert.Equal(t, tt.expectedResources, container.Resources)
		})
	}
}
