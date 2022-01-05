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
	"strings"

	"github.com/percona/percona-server-mysql-operator/pkg/platform"
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
	CRVersion             string           `json:"crVersion,omitempty"`
	Pause                 bool             `json:"pause,omitempty"`
	SecretsName           string           `json:"secretsName,omitempty"`
	SSLSecretName         string           `json:"sslSecretName,omitempty"`
	SSLInternalSecretName string           `json:"sslInternalSecretName,omitempty"`
	MySQL                 MySQLSpec        `json:"mysql,omitempty"`
	Orchestrator          OrchestratorSpec `json:"orchestrator,omitempty"`
	PMM                   *PMMSpec         `json:"pmm,omitempty"`
}

type ClusterType string

const (
	ClusterTypeGr    ClusterType = "gr"
	ClusterTypeAsync ClusterType = "async"
)

type MySQLSpec struct {
	ClusterType  ClusterType        `json:"clusterType,omitempty"`
	SizeSemiSync intstr.IntOrString `json:"sizeSemiSync,omitempty"`
	SemiSyncType string             `json:"semiSyncType,omitempty"`
	Expose       ServiceExpose      `json:"expose,omitempty"`

	Sidecars       []corev1.Container `json:"sidecars,omitempty"`
	SidecarVolumes []corev1.Volume    `json:"sidecarVolumes,omitempty"`
	SidecarPVCs    []SidecarPVC       `json:"sidecarPVCs,omitempty"`

	Configuration string `json:"configuration,omitempty"`

	PodSpec `json:",inline"`
}

type SidecarPVC struct {
	Name string `json:"name"`

	Spec corev1.PersistentVolumeClaimSpec `json:"spec"`
}

type OrchestratorSpec struct {
	PodSpec `json:",inline"`
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
	RuntimeClassName              *string                                 `json:"runtimeClassName,omitempty"`
}

type PMMSpec struct {
	Enabled                  bool                    `json:"enabled,omitempty"`
	Image                    string                  `json:"image,omitempty"`
	ServerHost               string                  `json:"serverHost,omitempty"`
	ServerUser               string                  `json:"serverUser,omitempty"`
	Resources                *PodResources           `json:"resources,omitempty"`
	ContainerSecurityContext *corev1.SecurityContext `json:"containerSecurityContext,omitempty"`
	ImagePullPolicy          corev1.PullPolicy       `json:"imagePullPolicy,omitempty"`
	RuntimeClassName         *string                 `json:"runtimeClassName,omitempty"`
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

type StatefulAppState string

const (
	StateInitializing StatefulAppState = "initializing"
	StateReady        StatefulAppState = "ready"
)

type StatefulAppStatus struct {
	Size  int32            `json:"size,omitempty"`
	Ready int32            `json:"ready,omitempty"`
	State StatefulAppState `json:"state,omitempty"`
}

// PerconaServerForMySQLStatus defines the observed state of PerconaServerForMySQL
type PerconaServerForMySQLStatus struct { // INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	MySQL        StatefulAppStatus `json:"mysql,omitempty"`
	Orchestrator StatefulAppStatus `json:"orchestrator,omitempty"`
}

// PerconaServerForMySQL is the Schema for the perconaserverformysqls API
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="MySQL",type=string,JSONPath=".status.mysql.state"
//+kubebuilder:printcolumn:name="Orchestrator",type=string,JSONPath=".status.orchestrator.state"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
//+kubebuilder:resource:scope=Namespaced
//+kubebuilder:resource:shortName=ps
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

type SystemUser string

const (
	UserRoot         SystemUser = "root"
	UserXtraBackup   SystemUser = "xtrabackup"
	UserMonitor      SystemUser = "monitor"
	UserClusterCheck SystemUser = "clustercheck"
	UserProxyAdmin   SystemUser = "proxyadmin"
	UserOperator     SystemUser = "operator"
	UserReplication  SystemUser = "replication"
	UserOrchestrator SystemUser = "orchestrator"
)

func (cr *PerconaServerForMySQL) MySQLSpec() *MySQLSpec {
	return &cr.Spec.MySQL
}

func (cr *PerconaServerForMySQL) PMMSpec() *PMMSpec {
	return cr.Spec.PMM
}

func (cr *PerconaServerForMySQL) OrchestratorSpec() *OrchestratorSpec {
	return &cr.Spec.Orchestrator
}

func (cr *PerconaServerForMySQL) CheckNSetDefaults(serverVersion *platform.ServerVersion) error {
	if cr.Spec.MySQL.SizeSemiSync.IntVal >= cr.Spec.MySQL.Size {
		return errors.New("mysql.sizeSemiSync can't be greater than or equal to mysql.size")
	}

	if cr.Spec.Orchestrator.Size > 1 {
		return errors.New("orchestrator size must be 1")
	}

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

	var fsgroup *int64
	if serverVersion.Platform != platform.PlatformOpenshift {
		var tp int64 = 1001
		fsgroup = &tp
	}
	sc := &corev1.PodSecurityContext{
		SupplementalGroups: []int64{1001},
		FSGroup:            fsgroup,
	}

	if cr.Spec.MySQL.PodSecurityContext == nil {
		cr.Spec.MySQL.PodSecurityContext = sc
	}

	if cr.Spec.Orchestrator.PodSecurityContext == nil {
		cr.Spec.Orchestrator.PodSecurityContext = sc
	}

	cr.Spec.MySQL.VolumeSpec = reconcileVol(cr.Spec.MySQL.VolumeSpec)
	cr.Spec.Orchestrator.VolumeSpec = reconcileVol(cr.Spec.Orchestrator.VolumeSpec)

	for i := range cr.Spec.MySQL.SidecarPVCs {
		defaultPVCSpec(&cr.Spec.MySQL.SidecarPVCs[i].Spec)
	}

	cr.Spec.MySQL.reconcileAffinityOpts()
	cr.Spec.Orchestrator.reconcileAffinityOpts()

	return nil
}

func reconcileVol(v *VolumeSpec) *VolumeSpec {
	if v == nil {
		v = &VolumeSpec{}
	}

	if v.EmptyDir == nil && v.HostPath == nil && v.PersistentVolumeClaim == nil {
		v.PersistentVolumeClaim = &corev1.PersistentVolumeClaimSpec{}
	}

	defaultPVCSpec(v.PersistentVolumeClaim)

	return v
}

func defaultPVCSpec(pvc *corev1.PersistentVolumeClaimSpec) {
	if pvc == nil {
		return
	}

	if len(pvc.AccessModes) == 0 {
		pvc.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}
}

const AffinityTopologyKeyNone = "none"

var affinityValidTopologyKeys = map[string]struct{}{
	AffinityTopologyKeyNone:         {},
	"kubernetes.io/hostname":        {},
	"topology.kubernetes.io/zone":   {},
	"topology.kubernetes.io/region": {},
}

var defaultAffinityTopologyKey = "kubernetes.io/hostname"

// reconcileAffinityOpts ensures that the affinity is set to the valid values.
// - if the affinity doesn't set at all - set topology key to `defaultAffinityTopologyKey`
// - if topology key is set and the value not the one of `affinityValidTopologyKeys` - set to `defaultAffinityTopologyKey`
// - if topology key set to value of `AffinityTopologyKeyNone` - disable the affinity at all
// - if `Advanced` affinity is set - leave everything as it is and set topology key to nil (Advanced options has a higher priority)
func (p *PodSpec) reconcileAffinityOpts() {
	switch {
	case p.Affinity == nil:
		p.Affinity = &PodAffinity{
			TopologyKey: &defaultAffinityTopologyKey,
		}

	case p.Affinity.TopologyKey == nil:
		p.Affinity.TopologyKey = &defaultAffinityTopologyKey

	case p.Affinity.Advanced != nil:
		p.Affinity.TopologyKey = nil

	case p.Affinity != nil && p.Affinity.TopologyKey != nil:
		if _, ok := affinityValidTopologyKeys[*p.Affinity.TopologyKey]; !ok {
			p.Affinity.TopologyKey = &defaultAffinityTopologyKey
		}
	}
}

func (p *PodSpec) GetAffinity(labels map[string]string) *corev1.Affinity {
	if p.Affinity == nil {
		return nil
	}

	switch {
	case p.Affinity.Advanced != nil:
		return p.Affinity.Advanced
	case p.Affinity.TopologyKey != nil:
		if strings.ToLower(*p.Affinity.TopologyKey) == AffinityTopologyKeyNone {
			return nil
		}
		return &corev1.Affinity{
			PodAntiAffinity: &corev1.PodAntiAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
					{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: labels,
						},
						TopologyKey: *p.Affinity.TopologyKey,
					},
				},
			},
		}
	}

	return nil
}

type AnnotationKey string

const (
	AnnotationSpecHash   AnnotationKey = "percona.com/last-applied-spec"
	AnnotationSecretHash AnnotationKey = "percona.com/last-applied-secret"
	AnnotationConfigHash AnnotationKey = "percona.com/last-applied-config"
)

const (
	NameLabel         = "app.kubernetes.io/name"
	InstanceLabel     = "app.kubernetes.io/instance"
	ManagedByLabel    = "app.kubernetes.io/managed-by"
	PartOfLabel       = "app.kubernetes.io/part-of"
	ComponentLabel    = "app.kubernetes.io/component"
	MySQLPrimaryLabel = "mysql.percona.com/primary"
	ExposedLabel      = "percona.com/exposed"
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

func (cr *PerconaServerForMySQL) InternalSecretName() string {
	return "internal-" + cr.Name
}

func (cr *PerconaServerForMySQL) PMMEnabled() bool {
	return cr.Spec.PMM != nil && cr.Spec.PMM.Enabled
}

func init() {
	SchemeBuilder.Register(&PerconaServerForMySQL{}, &PerconaServerForMySQLList{})
}
