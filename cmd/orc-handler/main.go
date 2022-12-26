package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
)

var log = logf.Log.WithName("orc-handler")

var (
	primary = flag.String("primary", "", "Primary hostname")
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	opts := zap.Options{
		Development: true,
		DestWriter:  os.Stdout,
	}
	logf.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	flag.Parse()

	if *primary == "" {
		log.Error(errors.New("primary flag should not be empty"), "failed to validate flags")
		os.Exit(1)
	}

	if err := setPrimaryLabel(ctx, *primary); err != nil {
		log.Error(err, "failed to set primary label")
		os.Exit(1)
	}
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

func setPrimaryLabel(ctx context.Context, primary string) error {
	l := log.WithName("setPrimaryLabel")

	ns, err := getNamespace()
	if err != nil {
		return errors.New("failed to get namespace")
	}

	crName, err := getClusterName()
	if err != nil {
		return errors.New("failed to get cluster name")
	}

	cl, err := newClient(ns)
	if err != nil {
		return err
	}

	serverVersion, err := platform.GetServerVersion()
	if err != nil {
		return err
	}
	cr, err := k8s.GetCRWithDefaults(ctx, cl, types.NamespacedName{
		Name:      crName,
		Namespace: ns,
	}, serverVersion)
	if err != nil {
		return err
	}

	primaryName := strings.TrimSuffix(strings.TrimSuffix(primary, "."+ns), "."+mysql.ServiceName(cr))

	pods, err := k8s.PodsByLabels(ctx, cl, mysql.MatchLabels(cr))
	if err != nil {
		return errors.Wrap(err, "get MySQL pods")
	}

	if len(pods) == 0 {
		return errors.New("MySQL pods not found")
	}

	var primaryPod *corev1.Pod
	for i := range pods {
		if pods[i].Name == primaryName {
			primaryPod = &pods[i]
			continue
		}
		pod := pods[i].DeepCopy()
		if pod.GetLabels()[apiv1alpha1.MySQLPrimaryLabel] == "true" {
			k8s.RemoveLabel(pod, apiv1alpha1.MySQLPrimaryLabel)
			if err := cl.Patch(ctx, pod, client.StrategicMergeFrom(&pods[i])); err != nil {
				return errors.Wrapf(err, "remove label from old primary pod: %v/%v", pod.GetNamespace(), pod.GetName())
			}

			l.Info(fmt.Sprintf("removed label from old primary pod: %v/%v", pod.GetNamespace(), pod.GetName()))
		}
	}

	if primaryPod == nil {
		return errors.Wrapf(err, "primary pod %s not found %s", primaryName, primary)
	}

	if primaryPod.GetLabels()[apiv1alpha1.MySQLPrimaryLabel] == "true" {
		l.Info(fmt.Sprintf("primary %v is not changed. skip", primaryName))
		return nil
	}

	pod := primaryPod.DeepCopy()
	k8s.AddLabel(pod, apiv1alpha1.MySQLPrimaryLabel, "true")
	if err := cl.Patch(ctx, pod, client.StrategicMergeFrom(primaryPod)); err != nil {
		return errors.Wrapf(err, "add label to new primary pod %v/%v", pod.GetNamespace(), pod.GetName())
	}

	l.Info(fmt.Sprintf("added label to new primary pod: %v/%v", pod.GetNamespace(), pod.GetName()))
	return nil
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
	if err := apiv1alpha1.AddToScheme(scheme); err != nil {
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
