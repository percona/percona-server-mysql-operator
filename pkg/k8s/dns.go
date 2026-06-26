package k8s

import (
	"context"
	"net"
	"strings"
	"sync"
)

var clusterDomain = sync.OnceValue(func() string {
	api := "kubernetes.default.svc"
	cname, err := net.DefaultResolver.LookupCNAME(context.Background(), api)
	if err != nil {
		return "cluster.local"
	}
	return strings.TrimSuffix(strings.TrimPrefix(cname, api+"."), ".")
})

// KubernetesClusterDomain looks up the Kubernetes cluster domain name.
// If the override parameter is provided, it is returned without performing any operations
func KubernetesClusterDomain(ctx context.Context, override string) string {
	if override != "" {
		return override
	}

	return clusterDomain()
}
