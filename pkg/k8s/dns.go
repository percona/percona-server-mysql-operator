package k8s

import (
	"context"
	"net"
	"strings"
)

// KubernetesClusterDomain looks up the Kubernetes cluster domain name.
// If the override parameter is provided, it is returned without performing any operations
func KubernetesClusterDomain(ctx context.Context, override string) string {
	if override != "" {
		return override
	}

	// Lookup an existing Service to determine its fully qualified domain name.
	// This is inexpensive because the "net" package uses OS-level DNS caching.
	// - https://golang.org/issue/24796
	api := "kubernetes.default.svc"
	cname, err := net.DefaultResolver.LookupCNAME(ctx, api)
	if err != nil {
		// The kubeadm default is "cluster.local" and is adequate when not running
		// in an actual Kubernetes cluster.
		return "cluster.local"
	}

	return strings.TrimSuffix(strings.TrimPrefix(cname, api+"."), ".")
}
