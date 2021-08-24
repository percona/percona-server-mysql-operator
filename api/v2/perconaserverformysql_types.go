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
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
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
	ReadinessInitialDelaySeconds  *int32                                  `json:"readinessDelaySec,omitempty"`
	ReadinessProbes               corev1.Probe                            `json:"readinessProbes,omitempty"`
	LivenessInitialDelaySeconds   *int32                                  `json:"livenessDelaySec,omitempty"`
	LivenessProbes                corev1.Probe                            `json:"livenessProbes,omitempty"`
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

func (cr *PerconaServerForMySQL) CheckNSetDefaults(log logr.Logger) {

}

func init() {
	SchemeBuilder.Register(&PerconaServerForMySQL{}, &PerconaServerForMySQLList{})
}
