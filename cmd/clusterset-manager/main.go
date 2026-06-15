package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/clusterset"
	csmanager "github.com/percona/percona-server-mysql-operator/pkg/clusterset/manager"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type replicaInitArgs struct {
	replicaClusterName string
	replicaEndpoint    string
	replicaPort        int
	recoveryMethod     string
	psClusterSetName   string
	namespace          string
	user               string
}

type replicaManager interface {
	CreateClusterSet(ctx context.Context, clustersetName string, sslMode apiv1.ClusterSetSSLMode) error
	CreateReplicaCluster(ctx context.Context, cluster *apiv1.ClusterSetCluster, recoverMethod string) error
	RemoveReplicaCluster(ctx context.Context, clusterName string, force bool) error
	SetPrimaryCluster(ctx context.Context, clusterName string) error
	ForcePrimaryCluster(ctx context.Context, clusterName string) error
}

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("usage: %s <command> [flags]", os.Args[0])
	}

	args := replicaInitArgs{}
	flag.StringVar(&args.replicaClusterName, "replica-cluster-name", "", "Replica cluster name")
	flag.StringVar(&args.replicaEndpoint, "replica-endpoint", "", "Replica endpoint")
	flag.IntVar(&args.replicaPort, "replica-port", 3306, "Replica port")
	flag.StringVar(&args.user, "user", "root", "User")
	flag.StringVar(&args.psClusterSetName, "ps-cluster-set-name", "", "PerconaServerMySQLClusterSet name")
	flag.StringVar(&args.namespace, "namespace", "", "Namespace")
	flag.StringVar(&args.recoveryMethod, "recovery-method", "", "Recovery method")

	if err := flag.CommandLine.Parse(os.Args[2:]); err != nil {
		log.Fatalf("failed to parse flags: %v", err)
	}

	ctx := context.Background()

	config, err := ctrl.GetConfig()
	if err != nil {
		log.Fatalf("failed to get config: %v", err)
	}

	cl, err := newClient(config)
	if err != nil {
		log.Fatalf("failed to create client: %v", err)
	}

	psClusterSet := &apiv1.PerconaServerMySQLClusterSet{}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: args.namespace, Name: args.psClusterSetName}, psClusterSet); err != nil {
		log.Fatalf("failed to get PerconaServerMySQLClusterSet: %v", err)
	}
	psClusterSet.SetDefaults()

	cliCmd, err := clientcmd.NewClient()
	if err != nil {
		log.Fatalf("failed to create clientcmd: %v", err)
	}

	pod, err := getSelfPod(ctx, cl)
	if err != nil {
		log.Fatalf("failed to get self pod: %v", err)
	}

	manager, err := csmanager.NewWithShellExec(ctx, psClusterSet, pod, &csmanager.ManagerOptions{
		Client:    cl,
		ClientCmd: cliCmd,
		// mysqlsh reports progress on stderr; send both streams to stdout for job logs.
		Stdout: os.Stdout,
		Stderr: os.Stdout,
	})
	if err != nil {
		log.Fatalf("failed to create manager: %v", err)
	}

	switch os.Args[1] {
	case clusterset.CmdCreateClusterSet:
		if err := createClusterSet(ctx, manager, args); err != nil {
			log.Fatalf("failed to create cluster set: %v", err)
		}
	case clusterset.CmdAddReplica:
		if err := addReplica(ctx, manager, args); err != nil {
			log.Fatalf("failed to add replica: %v", err)
		}
	case clusterset.CmdRemoveReplica:
		force := psClusterSet.Spec.UnsafeClusterSetFlags.ForcedClusterRemoval != nil && *psClusterSet.Spec.UnsafeClusterSetFlags.ForcedClusterRemoval
		if err := removeReplica(ctx, manager, args, force); err != nil {
			log.Fatalf("failed to remove replica: %v", err)
		}
	case clusterset.CmdSetPrimary:
		if err := setPrimary(ctx, manager, psClusterSet.Spec.PrimaryCluster); err != nil {
			log.Fatalf("failed to set primary cluster: %v", err)
		}
	case clusterset.CmdForcePrimary:
		if err := forcePrimary(ctx, manager, psClusterSet.Spec.PrimaryCluster); err != nil {
			log.Fatalf("failed to force primary cluster: %v", err)
		}
	default:
		log.Fatalf("invalid command: %s", os.Args[1])
	}
}

func forcePrimary(ctx context.Context, manager replicaManager, clusterName string) error {
	log.Printf("Forcefully setting primary cluster to '%s'", clusterName)
	if err := manager.ForcePrimaryCluster(ctx, clusterName); err != nil {
		return errors.Wrap(err, "failed to force primary cluster")
	}
	log.Printf("Primary cluster '%s' forcefully set", clusterName)
	return nil
}

func createClusterSet(ctx context.Context, manager replicaManager, args replicaInitArgs) error {
	log.Printf("Creating cluster set '%s'", args.psClusterSetName)
	if err := manager.CreateClusterSet(ctx, args.psClusterSetName, apiv1.ClusterSetSSLModeAuto); err != nil {
		return errors.Wrap(err, "failed to create cluster set")
	}
	log.Printf("Cluster set '%s' created", args.psClusterSetName)
	return nil

}

func addReplica(ctx context.Context, manager replicaManager, args replicaInitArgs) error {
	log.Printf("Adding replica cluster '%s' to clusterset '%s'", args.replicaClusterName, args.psClusterSetName)
	if err := manager.CreateReplicaCluster(ctx, &apiv1.ClusterSetCluster{
		InnoDBClusterName: args.replicaClusterName,
		Endpoints: []apiv1.ClusterSetClusterEndpoint{
			{
				Host: args.replicaEndpoint,
				Port: new(int32(args.replicaPort)),
			},
		},
	}, args.recoveryMethod); err != nil {
		return errors.Wrap(err, "failed to create replica cluster")
	}
	log.Printf("Replica cluster '%s' added to clusterset '%s'", args.replicaClusterName, args.psClusterSetName)
	return nil
}

func removeReplica(ctx context.Context, manager replicaManager, args replicaInitArgs, force bool) error {
	log.Printf("Removing replica cluster '%s' from clusterset '%s'", args.replicaClusterName, args.psClusterSetName)
	if err := manager.RemoveReplicaCluster(ctx, args.replicaClusterName, force); err != nil {
		if strings.Contains(err.Error(), "does not exist or does not belong to the ClusterSet") {
			log.Println("Cluster already removed")
			return nil
		}
		return errors.Wrap(err, "failed to remove replica cluster")
	}
	log.Printf("Replica cluster '%s' removed from clusterset '%s'", args.replicaClusterName, args.psClusterSetName)
	return nil
}

func setPrimary(ctx context.Context, manager replicaManager, primary string) error {
	log.Printf("Setting primary cluster to '%s'", primary)
	if err := manager.SetPrimaryCluster(ctx, primary); err != nil {
		if strings.Contains(err.Error(), "MYSQLSH 51317") {
			return nil
		}
		return errors.Wrap(err, "failed to set primary cluster")
	}
	return nil
}

func newClient(config *rest.Config) (client.Client, error) {
	scheme, err := newScheme()
	if err != nil {
		return nil, err
	}
	cl, err := client.New(config, client.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, fmt.Errorf("create controller-runtime client: %w", err)
	}
	return cl, nil
}

func newScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add client-go types to scheme: %w", err)
	}
	if err := apiv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add percona types to scheme: %w", err)
	}
	return scheme, nil
}

func getSelfPod(ctx context.Context, cl client.Client) (*corev1.Pod, error) {
	podName := os.Getenv("POD_NAME")
	podNamespace := os.Getenv("POD_NAMESPACE")
	if podName == "" || podNamespace == "" {
		return nil, errors.New("POD_NAME and POD_NAMESPACE must be set")
	}
	pod := &corev1.Pod{}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: podNamespace, Name: podName}, pod); err != nil {
		return nil, errors.Wrap(err, "failed to get self pod")
	}
	return pod, nil
}
