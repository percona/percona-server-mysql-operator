package k8s

import (
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"testing"
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
		inputVolumes    []corev1.VolumeMount
		expectedVolumes []corev1.VolumeMount
	}{
		"default volumes": {
			expectedVolumes: expectedVolumeMounts,
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
					MountPath: "dataMountPath"}),
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			container := InitContainer(componentName, image, pullPolicy, secCtx, expectedResources, tt.inputVolumes)

			assert.Equal(t, componentName+"-init", container.Name)
			assert.Equal(t, image, container.Image)
			assert.Equal(t, pullPolicy, container.ImagePullPolicy)
			assert.Equal(t, tt.expectedVolumes, container.VolumeMounts)
			assert.Equal(t, expectedCommand, container.Command)
			assert.Equal(t, expectedTerminationMessagePath, container.TerminationMessagePath)
			assert.Equal(t, expectedTerminationMessagePolicy, container.TerminationMessagePolicy)
			assert.Equal(t, secCtx, container.SecurityContext)
			assert.Equal(t, expectedResources, container.Resources)
		})
	}
}
