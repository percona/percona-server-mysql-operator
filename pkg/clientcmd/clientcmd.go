package clientcmd

import (
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Client struct {
	client     corev1client.CoreV1Interface
	restconfig *restclient.Config
}

func NewClient() (*Client, error) {
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

	return &Client{
		client:     cl,
		restconfig: restconfig,
	}, nil
}

func (c *Client) REST() restclient.Interface {
	return c.client.RESTClient()
}
