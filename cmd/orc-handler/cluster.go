package main

import (
	"context"
	"os"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	perconaClientCmd "github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
)

// cluster is the cluster orc-handler runs in.
type cluster struct {
	cr     *apiv1.PerconaServerMySQL
	client client.Client
	cliCmd perconaClientCmd.Client
}

func connect(ctx context.Context) (*cluster, error) {
	ns, err := getNamespace()
	if err != nil {
		return nil, errors.New("failed to get namespace")
	}

	crName, err := getClusterName()
	if err != nil {
		return nil, errors.New("failed to get cluster name")
	}

	cl, err := newClient(ns)
	if err != nil {
		return nil, err
	}

	cliCmd, err := perconaClientCmd.NewClient()
	if err != nil {
		return nil, err
	}

	serverVersion, err := platform.GetServerVersion(cliCmd)
	if err != nil {
		return nil, err
	}

	cr, err := k8s.GetCRWithDefaults(ctx, cl, types.NamespacedName{
		Name:      crName,
		Namespace: ns,
	}, serverVersion)
	if err != nil {
		return nil, err
	}

	return &cluster{cr: cr, client: cl, cliCmd: cliCmd}, nil
}

func podName(cr *apiv1.PerconaServerMySQL, host string) string {
	return strings.TrimSuffix(strings.TrimSuffix(host, "."+cr.Namespace), "."+mysql.ServiceName(cr))
}

func (c *cluster) pod(ctx context.Context, host string) (*corev1.Pod, error) {
	name := podName(c.cr, host)

	pod := new(corev1.Pod)
	nn := types.NamespacedName{Name: name, Namespace: c.cr.Namespace}
	if err := c.client.Get(ctx, nn, pod); err != nil {
		return nil, errors.Wrapf(err, "get pod %s", name)
	}

	return pod, nil
}

func getNamespace() (string, error) {
	ns, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", errors.Wrap(err, "read namespace file")
	}

	return string(ns), nil
}

func getClusterName() (string, error) {
	value, ok := os.LookupEnv("CLUSTER_NAME")
	if !ok {
		return "", errors.New("CLUSTER_NAME env var is not set")
	}
	return value, nil
}

func newClient(namespace string) (client.Client, error) {
	kubeconfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{
			Timeout: "10s",
		},
	).ClientConfig()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get client config")
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, errors.Wrap(err, "failed to add to client-go types to scheme")
	}
	if err := apiv1.AddToScheme(scheme); err != nil {
		return nil, errors.Wrap(err, "failed to add to percona types to scheme")
	}

	cl, err := client.New(kubeconfig, client.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to create client")
	}

	return client.NewNamespacedClient(cl, namespace), nil
}
