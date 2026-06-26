package platform

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	k8sversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/rest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
)

var log = logf.Log.WithName("platform")

type Platform string

const (
	PlatformUndef      Platform = ""
	PlatformKubernetes Platform = "kubernetes"
	PlatformOpenshift  Platform = "openshift"
)

type CloudProvider string

const (
	CloudProviderUndef CloudProvider = ""
	CloudProviderGKE   CloudProvider = "gke"
	CloudProviderEKS   CloudProvider = "eks"
	CloudProviderAKS   CloudProvider = "aks"
	CloudProviderDOKS  CloudProvider = "doks"
)

type ServerVersion struct {
	Platform      Platform
	CloudProvider CloudProvider
	Info          k8sversion.Info
}

// String returns the platform identifier used in telemetry and version
// service reports. When a cloud provider is detected it is appended as
// a suffix (e.g. "kubernetes-gke", "kubernetes-doks").
func (s *ServerVersion) String() string {
	if s == nil {
		return ""
	}
	if s.CloudProvider == CloudProviderUndef {
		return string(s.Platform)
	}
	return string(s.Platform) + "-" + string(s.CloudProvider)
}

var (
	cVersion *ServerVersion
	mx       sync.Mutex
)

func GetServerVersion(cliCmd clientcmd.Client) (*ServerVersion, error) {
	mx.Lock()
	defer mx.Unlock()
	if cVersion != nil {
		return cVersion, nil
	}

	v, err := getServerVersion(cliCmd)
	if err != nil {
		return nil, err
	}

	cVersion = v

	return cVersion, nil
}

func getServerVersion(cliCmd clientcmd.Client) (*ServerVersion, error) {
	var err error
	client := cliCmd.REST()

	version := &ServerVersion{}
	// openshift 4.0
	version.Info, err = probeAPI("/apis/quota.openshift.io", client)
	if err == nil {
		version.Platform = PlatformOpenshift
		version.Info.GitVersion = "undefined (v4.0+)"
		return version, nil
	}

	// k8s
	version.Info, err = probeAPI("/version", client)
	if err != nil {
		return version, err
	}
	version.Platform = PlatformKubernetes
	version.CloudProvider = detectCloudProvider(client)

	return version, nil
}

func detectCloudProvider(client rest.Interface) CloudProvider {
	if _, err := probeAPI("/apis/cloud.google.com", client); err == nil {
		log.Info("cloud provider detected", "provider", CloudProviderGKE, "signal", "apigroup:cloud.google.com")
		return CloudProviderGKE
	} else {
		log.V(1).Info("cloud provider probe miss", "provider", CloudProviderGKE, "signal", "apigroup:cloud.google.com", "err", err.Error())
	}
	if _, err := probeAPI("/apis/vpcresources.k8s.aws", client); err == nil {
		log.Info("cloud provider detected", "provider", CloudProviderEKS, "signal", "apigroup:vpcresources.k8s.aws")
		return CloudProviderEKS
	} else {
		log.V(1).Info("cloud provider probe miss", "provider", CloudProviderEKS, "signal", "apigroup:vpcresources.k8s.aws", "err", err.Error())
	}
	cp := detectCloudProviderFromNode(client)
	if cp == CloudProviderUndef {
		log.Info("cloud provider not detected", "provider", "unknown")
	}
	return cp
}

func detectCloudProviderFromNode(client rest.Interface) CloudProvider {
	body, err := client.Get().AbsPath("/api/v1/nodes").Param("limit", "1").Do(context.TODO()).Raw()
	if err != nil {
		log.V(1).Info("cloud provider node probe failed", "err", err.Error())
		return CloudProviderUndef
	}

	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				ProviderID string `json:"providerID"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		log.V(1).Info("cloud provider node probe unmarshal failed", "err", err.Error())
		return CloudProviderUndef
	}
	if len(list.Items) == 0 {
		log.V(1).Info("cloud provider node probe returned no nodes")
		return CloudProviderUndef
	}

	node := list.Items[0]
	log.V(1).Info("cloud provider node probe sample",
		"node", node.Metadata.Name,
		"providerID", node.Spec.ProviderID,
		"labelCount", len(node.Metadata.Labels),
	)

	for k := range node.Metadata.Labels {
		switch {
		case strings.HasPrefix(k, "doks.digitalocean.com/"):
			log.Info("cloud provider detected", "provider", CloudProviderDOKS, "signal", "label:"+k)
			return CloudProviderDOKS
		case strings.HasPrefix(k, "kubernetes.azure.com/"):
			log.Info("cloud provider detected", "provider", CloudProviderAKS, "signal", "label:"+k)
			return CloudProviderAKS
		}
	}

	switch {
	case strings.HasPrefix(node.Spec.ProviderID, "digitalocean://"):
		log.Info("cloud provider detected", "provider", CloudProviderDOKS, "signal", "providerID:digitalocean://")
		return CloudProviderDOKS
	case strings.HasPrefix(node.Spec.ProviderID, "azure://"):
		log.Info("cloud provider detected", "provider", CloudProviderAKS, "signal", "providerID:azure://")
		return CloudProviderAKS
	}

	return CloudProviderUndef
}

func probeAPI(path string, client rest.Interface) (k8sversion.Info, error) {
	var vInfo k8sversion.Info
	vBody, err := client.Get().AbsPath(path).Do(context.TODO()).Raw()
	if err != nil {
		return vInfo, err
	}

	err = json.Unmarshal(vBody, &vInfo)
	if err != nil {
		return vInfo, err
	}

	return vInfo, nil
}
