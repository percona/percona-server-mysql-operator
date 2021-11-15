/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v2

import (
	"fmt"
	"hash/fnv"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PerconaServerForMySQLSpec defines the desired state of PerconaServerForMySQL
type PerconaServerForMySQLSpec struct {
	CRVersion             string    `json:"crVersion,omitempty"`
	Pause                 bool      `json:"pause,omitempty"`
	SecretsName           string    `json:"secretsName,omitempty"`
	SSLSecretName         string    `json:"sslSecretName,omitempty"`
	SSLInternalSecretName string    `json:"sslInternalSecretName,omitempty"`
	MySQL                 MySQLSpec `json:"mysql,omitempty"`
	Orchestrator          PodSpec   `json:"orchestrator,omitempty"`
}

type ClusterType string

const (
	ClusterTypeGr    ClusterType = "gr"
	ClusterTypeAsync ClusterType = "async"
)

type MySQLSpec struct {
	ClusterType  ClusterType   `json:"clusterType,omitempty"`
	SizeSemiSync int32         `json:"sizeSemiSync,omitempty"`
	SemiSyncType string        `json:"semiSyncType,omitempty"`
	Expose       ServiceExpose `json:"expose,omitempty"`
	PodSpec      `json:",inline"`
}

type PodSpec struct {
	Size                          int32                                   `json:"size,omitempty"`
	Image                         string                                  `json:"image,omitempty"`
	Resources                     *PodResources                           `json:"resources,omitempty"`
	SidecarResources              *PodResources                           `json:"sidecarResources,omitempty"`
	VolumeSpec                    *VolumeSpec                             `json:"volumeSpec,omitempty"`
	Affinity                      *PodAffinity                            `json:"affinity,omitempty"`
	NodeSelector                  map[string]string                       `json:"nodeSelector,omitempty"`
	Tolerations                   []corev1.Toleration                     `json:"tolerations,omitempty"`
	PriorityClassName             string                                  `json:"priorityClassName,omitempty"`
	Annotations                   map[string]string                       `json:"annotations,omitempty"`
	Labels                        map[string]string                       `json:"labels,omitempty"`
	ImagePullSecrets              []corev1.LocalObjectReference           `json:"imagePullSecrets,omitempty"`
	Configuration                 string                                  `json:"configuration,omitempty"`
	PodDisruptionBudget           *PodDisruptionBudgetSpec                `json:"podDisruptionBudget,omitempty"`
	VaultSecretName               string                                  `json:"vaultSecretName,omitempty"`
	SSLSecretName                 string                                  `json:"sslSecretName,omitempty"`
	SSLInternalSecretName         string                                  `json:"sslInternalSecretName,omitempty"`
	EnvVarsSecretName             string                                  `json:"envVarsSecret,omitempty"`
	TerminationGracePeriodSeconds *int64                                  `json:"gracePeriod,omitempty"`
	ForceUnsafeBootstrap          bool                                    `json:"forceUnsafeBootstrap,omitempty"`
	ServiceType                   corev1.ServiceType                      `json:"serviceType,omitempty"`
	ReplicasServiceType           corev1.ServiceType                      `json:"replicasServiceType,omitempty"`
	ExternalTrafficPolicy         corev1.ServiceExternalTrafficPolicyType `json:"externalTrafficPolicy,omitempty"`
	ReplicasExternalTrafficPolicy corev1.ServiceExternalTrafficPolicyType `json:"replicasExternalTrafficPolicy,omitempty"`
	LoadBalancerSourceRanges      []string                                `json:"loadBalancerSourceRanges,omitempty"`
	ServiceAnnotations            map[string]string                       `json:"serviceAnnotations,omitempty"`
	SchedulerName                 string                                  `json:"schedulerName,omitempty"`
	StartupProbe                  corev1.Probe                            `json:"startupProbe,omitempty"`
	ReadinessProbe                corev1.Probe                            `json:"readinessProbe,omitempty"`
	LivenessProbe                 corev1.Probe                            `json:"livenessProbe,omitempty"`
	PodSecurityContext            *corev1.PodSecurityContext              `json:"podSecurityContext,omitempty"`
	ContainerSecurityContext      *corev1.SecurityContext                 `json:"containerSecurityContext,omitempty"`
	ServiceAccountName            string                                  `json:"serviceAccountName,omitempty"`
	ImagePullPolicy               corev1.PullPolicy                       `json:"imagePullPolicy,omitempty"`
	Sidecars                      []corev1.Container                      `json:"sidecars,omitempty"`
	RuntimeClassName              *string                                 `json:"runtimeClassName,omitempty"`
}

type PodDisruptionBudgetSpec struct {
	MinAvailable   *intstr.IntOrString `json:"minAvailable,omitempty"`
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`
}

type PodAffinity struct {
	TopologyKey *string          `json:"antiAffinityTopologyKey,omitempty"`
	Advanced    *corev1.Affinity `json:"advanced,omitempty"`
}

type VolumeSpec struct {
	// EmptyDir to use as data volume for mysql. EmptyDir represents a temporary
	// directory that shares a pod's lifetime.
	// +optional
	EmptyDir *corev1.EmptyDirVolumeSource `json:"emptyDir,omitempty"`

	// HostPath to use as data volume for mysql. HostPath represents a
	// pre-existing file or directory on the host machine that is directly
	// exposed to the container.
	// +optional
	HostPath *corev1.HostPathVolumeSource `json:"hostPath,omitempty"`

	// PersistentVolumeClaim to specify PVC spec for the volume for mysql data.
	// It has the highest level of precedence, followed by HostPath and
	// EmptyDir. And represents the PVC specification.
	// +optional
	PersistentVolumeClaim *corev1.PersistentVolumeClaimSpec `json:"persistentVolumeClaim,omitempty"`
}

type PodResources struct {
	Requests *ResourcesList `json:"requests,omitempty"`
	Limits   *ResourcesList `json:"limits,omitempty"`
}

type ResourcesList struct {
	Memory           string `json:"memory,omitempty"`
	CPU              string `json:"cpu,omitempty"`
	EphemeralStorage string `json:"ephemeral-storage,omitempty"`
}

type ServiceExpose struct {
	Enabled                  bool                                    `json:"enabled,omitempty"`
	Type                     corev1.ServiceType                      `json:"type,omitempty"`
	LoadBalancerSourceRanges []string                                `json:"loadBalancerSourceRanges,omitempty"`
	Annotations              map[string]string                       `json:"annotations,omitempty"`
	TrafficPolicy            corev1.ServiceExternalTrafficPolicyType `json:"trafficPolicy,omitempty"`
}

// PerconaServerForMySQLStatus defines the observed state of PerconaServerForMySQL
type PerconaServerForMySQLStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Namespaced
//+kubebuilder:resource:shortName=ps

// PerconaServerForMySQL is the Schema for the perconaserverformysqls API
type PerconaServerForMySQL struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PerconaServerForMySQLSpec   `json:"spec,omitempty"`
	Status PerconaServerForMySQLStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PerconaServerForMySQLList contains a list of PerconaServerForMySQL
type PerconaServerForMySQLList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PerconaServerForMySQL `json:"items"`
}

const (
	USERS_SECRET_KEY_ROOT         string = "root"
	USERS_SECRET_KEY_XTRABACKUP   string = "xtrabackup"
	USERS_SECRET_KEY_MONITOR      string = "monitor"
	USERS_SECRET_KEY_CLUSTERCHECK string = "clustercheck"
	USERS_SECRET_KEY_PROXYADMIN   string = "proxyadmin"
	USERS_SECRET_KEY_OPERATOR     string = "operator"
	USERS_SECRET_KEY_REPLICATION  string = "replication"
	USERS_SECRET_KEY_ORCHESTRATOR string = "orchestrator"
)

func (cr *PerconaServerForMySQL) CheckNSetDefaults(log logr.Logger) error {
	if cr.Spec.MySQL.StartupProbe.InitialDelaySeconds == 0 {
		cr.Spec.MySQL.StartupProbe.InitialDelaySeconds = 15
	}
	if cr.Spec.MySQL.StartupProbe.PeriodSeconds == 0 {
		cr.Spec.MySQL.StartupProbe.PeriodSeconds = 10
	}
	if cr.Spec.MySQL.StartupProbe.FailureThreshold == 0 {
		cr.Spec.MySQL.StartupProbe.FailureThreshold = 1
	}
	if cr.Spec.MySQL.StartupProbe.SuccessThreshold == 0 {
		cr.Spec.MySQL.StartupProbe.SuccessThreshold = 1
	}
	if cr.Spec.MySQL.StartupProbe.TimeoutSeconds == 0 {
		cr.Spec.MySQL.StartupProbe.TimeoutSeconds = 300
	}

	if cr.Spec.MySQL.LivenessProbe.InitialDelaySeconds == 0 {
		cr.Spec.MySQL.LivenessProbe.InitialDelaySeconds = 15
	}
	if cr.Spec.MySQL.LivenessProbe.PeriodSeconds == 0 {
		cr.Spec.MySQL.LivenessProbe.PeriodSeconds = 10
	}
	if cr.Spec.MySQL.LivenessProbe.FailureThreshold == 0 {
		cr.Spec.MySQL.LivenessProbe.FailureThreshold = 3
	}
	if cr.Spec.MySQL.LivenessProbe.SuccessThreshold == 0 {
		cr.Spec.MySQL.LivenessProbe.SuccessThreshold = 1
	}
	if cr.Spec.MySQL.LivenessProbe.TimeoutSeconds == 0 {
		cr.Spec.MySQL.LivenessProbe.TimeoutSeconds = 30
	}

	if cr.Spec.MySQL.ReadinessProbe.InitialDelaySeconds == 0 {
		cr.Spec.MySQL.ReadinessProbe.InitialDelaySeconds = 30
	}
	if cr.Spec.MySQL.ReadinessProbe.PeriodSeconds == 0 {
		cr.Spec.MySQL.ReadinessProbe.PeriodSeconds = 5
	}
	if cr.Spec.MySQL.ReadinessProbe.FailureThreshold == 0 {
		cr.Spec.MySQL.ReadinessProbe.FailureThreshold = 3
	}
	if cr.Spec.MySQL.ReadinessProbe.SuccessThreshold == 0 {
		cr.Spec.MySQL.ReadinessProbe.SuccessThreshold = 1
	}
	if cr.Spec.MySQL.ReadinessProbe.TimeoutSeconds == 0 {
		cr.Spec.MySQL.ReadinessProbe.TimeoutSeconds = 3
	}

	if cr.Spec.MySQL.PodSecurityContext == nil {
		var tp int64 = 1001
		cr.Spec.MySQL.PodSecurityContext = &corev1.PodSecurityContext{
			SupplementalGroups: []int64{tp},
			FSGroup:            &tp,
		}
	}

	if cr.Spec.Orchestrator.PodSecurityContext == nil {
		var tp int64 = 1001
		cr.Spec.Orchestrator.PodSecurityContext = &corev1.PodSecurityContext{
			SupplementalGroups: []int64{tp},
			FSGroup:            &tp,
		}
	}

	cr.Spec.MySQL.VolumeSpec.reconcile()
	cr.Spec.Orchestrator.VolumeSpec.reconcile()

	return nil
}

func (v *VolumeSpec) reconcile() {
	if v == nil {
		v = &VolumeSpec{}
	}

	if v.EmptyDir == nil && v.HostPath == nil && v.PersistentVolumeClaim == nil {
		v.PersistentVolumeClaim = &corev1.PersistentVolumeClaimSpec{}
	}

	if v.PersistentVolumeClaim != nil {
		if len(v.PersistentVolumeClaim.AccessModes) == 0 {
			v.PersistentVolumeClaim.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
		}
	}
}

const (
	NameLabel         = "app.kubernetes.io/name"
	InstanceLabel     = "app.kubernetes.io/instance"
	ManagedByLabel    = "app.kubernetes.io/managed-by"
	PartOfLabel       = "app.kubernetes.io/part-of"
	ComponentLabel    = "app.kubernetes.io/component"
	MySQLPrimaryLabel = "mysql.percona.com/primary"
)

func (cr *PerconaServerForMySQL) Labels() map[string]string {
	return map[string]string{
		NameLabel:      "percona-server",
		InstanceLabel:  cr.Name,
		ManagedByLabel: "percona-server-operator",
		PartOfLabel:    "percona-server",
	}
}

func (cr *PerconaServerForMySQL) ClusterHint() string {
	return fmt.Sprintf("%s.%s", cr.Name, cr.Namespace)
}

func GetClusterNameFromObject(obj client.Object) (string, error) {
	labels := obj.GetLabels()
	instance, ok := labels[InstanceLabel]
	if !ok {
		return "", errors.Errorf("label %s doesn't exist", InstanceLabel)
	}
	return instance, nil
}

// ClusterHash returns FNV hash of the CustomResource UID
func (cr *PerconaServerForMySQL) ClusterHash() string {
	serverIDHash := fnv.New32()
	serverIDHash.Write([]byte(string(cr.UID)))

	// We use only first 7 digits to give a space for pod number which is
	// appended to all server ids. If we don't do this, it can cause a
	// int32 overflow.
	// P.S max value is 4294967295
	serverIDHashStr := fmt.Sprint(serverIDHash.Sum32())

	if len(serverIDHashStr) > 7 {
		serverIDHashStr = serverIDHashStr[:7]
	}

	return serverIDHashStr
}

func init() {
	SchemeBuilder.Register(&PerconaServerForMySQL{}, &PerconaServerForMySQLList{})
}
