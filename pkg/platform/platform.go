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

// Managed AKS API server hostnames always end with .azmk8s.io; AKS does not
// register a single distinguishing API group, so we fingerprint by hostname.
const aksHostSuffix = ".azmk8s.io"

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
	version.CloudProvider = detectCloudProvider(client, cliCmd.Host())

	return version, nil
}

func detectCloudProvider(client rest.Interface, host string) CloudProvider {
	probes := []struct {
		provider CloudProvider
		group    string
	}{
		{CloudProviderGKE, "cloud.google.com"},
		{CloudProviderEKS, "vpcresources.k8s.aws"},
		{CloudProviderDOKS, "dataplane-operator.doks.digitalocean.com"},
	}
	for _, p := range probes {
		path := "/apis/" + p.group
		if _, err := probeAPI(path, client); err == nil {
			log.Info("cloud provider detected", "provider", p.provider, "signal", "apigroup:"+p.group)
			return p.provider
		} else {
			log.V(1).Info("cloud provider probe miss", "provider", p.provider, "signal", "apigroup:"+p.group, "err", err.Error())
		}
	}
	if strings.Contains(host, aksHostSuffix) {
		log.Info("cloud provider detected", "provider", CloudProviderAKS, "signal", "host:"+aksHostSuffix)
		return CloudProviderAKS
	}
	log.Info("cloud provider not detected", "provider", "unknown")
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
