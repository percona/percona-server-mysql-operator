package k8s

import (
	"context"
	"os"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

type ComponentWithInit interface {
	GetInitImage() string
}

func InitContainer(component, image string,
	pullPolicy corev1.PullPolicy,
	secCtx *corev1.SecurityContext,
	resources corev1.ResourceRequirements,
	extraVolumeMounts []corev1.VolumeMount,
) corev1.Container {

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      apiv1alpha1.BinVolumeName,
			MountPath: apiv1alpha1.BinVolumePath,
		},
	}

	if len(extraVolumeMounts) > 0 {
		volumeMounts = append(volumeMounts, extraVolumeMounts...)
	}

	return corev1.Container{
		Name:                     component + "-init",
		Image:                    image,
		ImagePullPolicy:          pullPolicy,
		VolumeMounts:             volumeMounts,
		Command:                  []string{"/opt/percona-server-mysql-operator/ps-init-entrypoint.sh"},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          secCtx,
		Resources:                resources,
	}
}

// InitImage returns the image to be used in init container.
// It returns component specific init image if it's defined, else it return top level init image.
// If there is no init image defined in the CR, it returns the current running operator image.
func InitImage(ctx context.Context, cl client.Reader, cr *apiv1alpha1.PerconaServerMySQL, comp ComponentWithInit) (string, error) {
	if image := comp.GetInitImage(); len(image) > 0 {
		return image, nil
	}
	if image := cr.Spec.InitImage; len(image) > 0 {
		return image, nil
	}
	return OperatorImage(ctx, cl)
}

func OperatorImage(ctx context.Context, cl client.Reader) (string, error) {
	pod, err := operatorPod(ctx, cl)
	if err != nil {
		return "", errors.Wrap(err, "get operator pod")
	}

	for _, container := range pod.Spec.Containers {
		if container.Name == "manager" {
			return container.Image, nil
		}
	}

	return "", errors.New("manager container not found")
}

func operatorPod(ctx context.Context, cl client.Reader) (*corev1.Pod, error) {
	ns, err := DefaultAPINamespace()
	if err != nil {
		return nil, errors.Wrap(err, "get namespace")
	}

	pod := &corev1.Pod{}
	nn := types.NamespacedName{
		Namespace: ns,
		Name:      os.Getenv("HOSTNAME"),
	}
	if err := cl.Get(ctx, nn, pod); err != nil {
		return nil, err
	}

	return pod, nil
}
