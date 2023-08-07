package clientcmd

import (
	"context"
	"io"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/scheme"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	controllerClient "sigs.k8s.io/controller-runtime/pkg/client"
)

type client struct {
	client     corev1client.CoreV1Interface
	restconfig *restclient.Config
}

type Client interface {
	Exec(ctx context.Context, obj controllerClient.Object, containerName string, command []string, stdin io.Reader, stdout, stderr io.Writer, tty bool) error
	REST() restclient.Interface
}

func NewClient() (Client, error) {
	// Instantiate loader for kubeconfig file.
	kubeconfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{
			Timeout: "10s",
		},
	)

	// Get a rest.Config from the kubeconfig file.  This will be passed into all
	// the client objects we create.
	restconfig, err := kubeconfig.ClientConfig()
	if err != nil {
		return nil, err
	}

	// Create a Kubernetes core/v1 client.
	cl, err := corev1client.NewForConfig(restconfig)
	if err != nil {
		return nil, err
	}

	return &client{
		client:     cl,
		restconfig: restconfig,
	}, nil
}

func (c *client) Exec(
	ctx context.Context,
	obj controllerClient.Object,
	containerName string,
	command []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	tty bool) error {

	var pod *corev1.Pod
	switch t := obj.(type) {
	case *corev1.Pod:
		pod = t
	case *corev1.Service:
		clientset, err := corev1client.NewForConfig(c.restconfig)
		if err != nil {
			return err
		}
		namespace := t.GetNamespace()
		if t.Spec.Selector == nil || len(t.Spec.Selector) == 0 {
			return errors.Errorf("invalid service '%s': Service is defined without a selector", t.Name)
		}
		selector := labels.SelectorFromSet(t.Spec.Selector)

		podList, err := clientset.Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: selector.String(),
		})
		if err != nil {
			return err
		}
		if len(podList.Items) == 0 {
			return errors.Errorf("invalid service '%s': no pods found", t.Name)
		}
		pod = &podList.Items[0]
	default:
		return errors.Errorf("invalid object type '%T'", obj)
	}

	req := c.client.RESTClient().
		Post().
		Namespace(pod.GetNamespace()).
		Resource("pods").
		Name(pod.GetName()).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   command,
			Stdin:     stdin != nil,
			Stdout:    stdout != nil,
			Stderr:    stderr != nil,
			TTY:       tty,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(c.restconfig, "POST", req.URL())
	if err != nil {
		return err
	}

	// Connect this process' std{in,out,err} to the remote shell process.
	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Tty:    tty,
	})

}

func (c *client) REST() restclient.Interface {
	return c.client.RESTClient()
}
