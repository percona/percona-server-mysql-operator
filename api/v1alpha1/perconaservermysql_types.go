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

package v1alpha1

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

// PerconaServerMySQLSpec defines the desired state of PerconaServerMySQL
type PerconaServerMySQLSpec struct {
	CRVersion             string           `json:"crVersion,omitempty"`
	Pause                 bool             `json:"pause,omitempty"`
	SecretsName           string           `json:"secretsName,omitempty"`
	SSLSecretName         string           `json:"sslSecretName,omitempty"`
	SSLInternalSecretName string           `json:"sslInternalSecretName,omitempty"`
	AllowUnsafeConfig     bool             `json:"allowUnsafeConfigurations,omitempty"`
	MySQL                 MySQLSpec        `json:"mysql,omitempty"`
	Orchestrator          OrchestratorSpec `json:"orchestrator,omitempty"`
	PMM                   *PMMSpec         `json:"pmm,omitempty"`
	Backup                *BackupSpec      `json:"backup,omitempty"`
	Router                *MySQLRouterSpec `json:"router,omitempty"`
}

type ClusterType string

const (
	ClusterTypeGR    ClusterType = "group-replication"
	ClusterTypeAsync ClusterType = "async"
)

type MySQLSpec struct {
	ClusterType  ClusterType            `json:"clusterType,omitempty"`
	SizeSemiSync intstr.IntOrString     `json:"sizeSemiSync,omitempty"`
	SemiSyncType string                 `json:"semiSyncType,omitempty"`
	Expose       ServiceExposeTogglable `json:"expose,omitempty"`

	Sidecars       []corev1.Container `json:"sidecars,omitempty"`
	SidecarVolumes []corev1.Volume    `json:"sidecarVolumes,omitempty"`
	SidecarPVCs    []SidecarPVC       `json:"sidecarPVCs,omitempty"`

	Configuration string `json:"configuration,omitempty"`

	PrimaryServiceType  corev1.ServiceType `json:"primaryServiceType,omitempty"`
	ReplicasServiceType corev1.ServiceType `json:"replicasServiceType,omitempty"`

	PodSpec `json:",inline"`
}

func (m MySQLSpec) IsAsync() bool {
	return m.ClusterType == ClusterTypeAsync
}

func (m MySQLSpec) IsGR() bool {
	return m.ClusterType == ClusterTypeGR
}

type SidecarPVC struct {
	Name string `json:"name"`

	Spec corev1.PersistentVolumeClaimSpec `json:"spec"`
}

type OrchestratorSpec struct {
	Expose ServiceExpose `json:"expose,omitempty"`

	PodSpec `json:",inline"`
}

type PodSpec struct {
	Size                          int32                                   `json:"size,omitempty"`
	Image                         string                                  `json:"image,omitempty"`
	Resources                     corev1.ResourceRequirements             `json:"resources,omitempty"`
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
	Enabled                  bool                        `json:"enabled,omitempty"`
	Image                    string                      `json:"image,omitempty"`
	ServerHost               string                      `json:"serverHost,omitempty"`
	ServerUser               string                      `json:"serverUser,omitempty"`
	Resources                corev1.ResourceRequirements `json:"resources,omitempty"`
	ContainerSecurityContext *corev1.SecurityContext     `json:"containerSecurityContext,omitempty"`
	ImagePullPolicy          corev1.PullPolicy           `json:"imagePullPolicy,omitempty"`
	RuntimeClassName         *string                     `json:"runtimeClassName,omitempty"`
}

type BackupSpec struct {
	Enabled                  bool                          `json:"enabled,omitempty"`
	Image                    string                        `json:"image,omitempty"`
	ImagePullSecrets         []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	ImagePullPolicy          corev1.PullPolicy             `json:"imagePullPolicy,omitempty"`
	ServiceAccountName       string                        `json:"serviceAccountName,omitempty"`
	ContainerSecurityContext *corev1.SecurityContext       `json:"containerSecurityContext,omitempty"`
	Resources                corev1.ResourceRequirements   `json:"resources,omitempty"`
	Storages                 map[string]*BackupStorageSpec `json:"storages,omitempty"`
}

type BackupStorageType string

const (
	BackupStorageFilesystem BackupStorageType = "filesystem"
	BackupStorageS3         BackupStorageType = "s3"
	BackupStorageGCS        BackupStorageType = "gcs"
	BackupStorageAzure      BackupStorageType = "azure"
)

type BackupStorageSpec struct {
	Type                     BackupStorageType           `json:"type"`
	Volume                   *VolumeSpec                 `json:"volumeSpec,omitempty"`
	S3                       *BackupStorageS3Spec        `json:"s3,omitempty"`
	GCS                      *BackupStorageGCSSpec       `json:"gcs,omitempty"`
	Azure                    *BackupStorageAzureSpec     `json:"azure,omitempty"`
	NodeSelector             map[string]string           `json:"nodeSelector,omitempty"`
	Resources                corev1.ResourceRequirements `json:"resources,omitempty"`
	Affinity                 *corev1.Affinity            `json:"affinity,omitempty"`
	Tolerations              []corev1.Toleration         `json:"tolerations,omitempty"`
	Annotations              map[string]string           `json:"annotations,omitempty"`
	Labels                   map[string]string           `json:"labels,omitempty"`
	SchedulerName            string                      `json:"schedulerName,omitempty"`
	PriorityClassName        string                      `json:"priorityClassName,omitempty"`
	PodSecurityContext       *corev1.PodSecurityContext  `json:"podSecurityContext,omitempty"`
	ContainerSecurityContext *corev1.SecurityContext     `json:"containerSecurityContext,omitempty"`
	RuntimeClassName         *string                     `json:"runtimeClassName,omitempty"`
	VerifyTLS                *bool                       `json:"verifyTLS,omitempty"`
}

type BackupStorageS3Spec struct {
	Bucket            string `json:"bucket"`
	CredentialsSecret string `json:"credentialsSecret"`
	Region            string `json:"region,omitempty"`
	EndpointURL       string `json:"endpointUrl,omitempty"`
	StorageClass      string `json:"storageClass,omitempty"`
}

type BackupStorageGCSSpec struct {
	Bucket            string `json:"bucket"`
	CredentialsSecret string `json:"credentialsSecret"`
	EndpointURL       string `json:"endpointUrl,omitempty"`

	// STANDARD, NEARLINE, COLDLINE, ARCHIVE
	StorageClass string `json:"storageClass,omitempty"`
}

type BackupStorageAzureSpec struct {
	// A container name is a valid DNS name that conforms to the Azure naming rules.
	ContainerName string `json:"containerName"`

	// A generated key that can be used to authorize access to data in your account using the Shared Key authorization.
	CredentialsSecret string `json:"credentialsSecret"`

	// The endpoint allows clients to securely access data
	EndpointURL string `json:"endpointUrl,omitempty"`

	// Hot (Frequently accessed or modified data), Cool (Infrequently accessed or modified data), Archive (Rarely accessed or modified data)
	StorageClass string `json:"storageClass,omitempty"`
}

type MySQLRouterSpec struct {
	Expose ServiceExpose `json:"expose,omitempty"`

	PodSpec `json:",inline"`
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

type ServiceExpose struct {
	Type                     corev1.ServiceType                      `json:"type,omitempty"`
	LoadBalancerSourceRanges []string                                `json:"loadBalancerSourceRanges,omitempty"`
	Annotations              map[string]string                       `json:"annotations,omitempty"`
	TrafficPolicy            corev1.ServiceExternalTrafficPolicyType `json:"trafficPolicy,omitempty"`
}

type ServiceExposeTogglable struct {
	Enabled bool `json:"enabled,omitempty"`

	ServiceExpose `json:",inline"`
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

// PerconaServerMySQLStatus defines the observed state of PerconaServerMySQL
type PerconaServerMySQLStatus struct { // INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	MySQL        StatefulAppStatus `json:"mysql,omitempty"`
	Orchestrator StatefulAppStatus `json:"orchestrator,omitempty"`
	Router       StatefulAppStatus `json:"router,omitempty"`
	State        StatefulAppState  `json:"state,omitempty"`
	// +optional
	Host string `json:"host"`
}

// PerconaServerMySQL is the Schema for the perconaservermysqls API
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Replication",type=string,JSONPath=".spec.mysql.clusterType"
//+kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=".status.host"
//+kubebuilder:printcolumn:name="State",type=string,JSONPath=".status.state"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
//+kubebuilder:resource:scope=Namespaced
//+kubebuilder:resource:shortName=ps
type PerconaServerMySQL struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PerconaServerMySQLSpec   `json:"spec,omitempty"`
	Status PerconaServerMySQLStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PerconaServerMySQLList contains a list of PerconaServerMySQL
type PerconaServerMySQLList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PerconaServerMySQL `json:"items"`
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
	UserPMMServer    SystemUser = "pmmserver"
)

func (cr *PerconaServerMySQL) MySQLSpec() *MySQLSpec {
	return &cr.Spec.MySQL
}

func (cr *PerconaServerMySQL) PMMSpec() *PMMSpec {
	return cr.Spec.PMM
}

func (cr *PerconaServerMySQL) OrchestratorSpec() *OrchestratorSpec {
	return &cr.Spec.Orchestrator
}

func (cr *PerconaServerMySQL) CheckNSetDefaults(serverVersion *platform.ServerVersion) error {
	if len(cr.Spec.Backup.Image) == 0 {
		return errors.New("backup.image can't be empty")
	}

	if cr.Spec.MySQL.Size != 0 && cr.Spec.MySQL.SizeSemiSync.IntVal >= cr.Spec.MySQL.Size {
		return errors.New("mysql.sizeSemiSync can't be greater than or equal to mysql.size")
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

	var err error
	cr.Spec.MySQL.VolumeSpec, err = reconcileVol(cr.Spec.MySQL.VolumeSpec)
	if err != nil {
		return errors.Wrap(err, "reconcile mysql volumeSpec")
	}

	for i := range cr.Spec.MySQL.SidecarPVCs {
		defaultPVCSpec(&cr.Spec.MySQL.SidecarPVCs[i].Spec)
	}

	cr.Spec.MySQL.reconcileAffinityOpts()
	cr.Spec.Orchestrator.reconcileAffinityOpts()

	if oSize := int(cr.Spec.Orchestrator.Size); (oSize < 3 || oSize%2 == 0) && oSize != 0 && !cr.Spec.AllowUnsafeConfig {
		return errors.New("Orchestrator size must be 3 or greater and an odd number for raft setup")
	}

	if cr.Spec.MySQL.ClusterType == ClusterTypeGR && cr.Spec.Router == nil {
		return errors.New("router section is needed for group replication")
	}

	if cr.Spec.Pause {
		cr.Spec.MySQL.Size = 0
		cr.Spec.Orchestrator.Size = 0
		cr.Spec.Router.Size = 0
	}

	return nil
}

func reconcileVol(v *VolumeSpec) (*VolumeSpec, error) {
	if v == nil || v.EmptyDir == nil && v.HostPath == nil && v.PersistentVolumeClaim == nil {
		return nil, errors.New("volumeSpec and it's internals should be specified")
	}
	if v.PersistentVolumeClaim == nil {
		return nil, errors.New("pvc should be specified")
	}
	_, limits := v.PersistentVolumeClaim.Resources.Limits[corev1.ResourceStorage]
	_, requests := v.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage]
	if !(limits || requests) {
		return nil, errors.New("pvc's resources.limits[storage] or resources.requests[storage] should be specified")
	}

	defaultPVCSpec(v.PersistentVolumeClaim)

	return v, nil
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

func (cr *PerconaServerMySQL) Labels() map[string]string {
	return map[string]string{
		NameLabel:      "percona-server",
		InstanceLabel:  cr.Name,
		ManagedByLabel: "percona-server-operator",
		PartOfLabel:    "percona-server",
	}
}

func (cr *PerconaServerMySQL) ClusterHint() string {
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

func FNVHash(p []byte) string {
	hash := fnv.New32()
	hash.Write(p)
	return fmt.Sprint(hash.Sum32())
}

// ClusterHash returns FNV hash of the CustomResource UID
func (cr *PerconaServerMySQL) ClusterHash() string {
	serverIDHash := FNVHash([]byte(string(cr.UID)))

	// We use only first 7 digits to give a space for pod number which is
	// appended to all server ids. If we don't do this, it can cause a
	// int32 overflow.
	// P.S max value is 4294967295
	if len(serverIDHash) > 7 {
		serverIDHash = serverIDHash[:7]
	}

	return serverIDHash
}

func (cr *PerconaServerMySQL) InternalSecretName() string {
	return "internal-" + cr.Name
}

func (cr *PerconaServerMySQL) PMMEnabled() bool {
	return cr.Spec.PMM != nil && cr.Spec.PMM.Enabled
}

func init() {
	SchemeBuilder.Register(&PerconaServerMySQL{}, &PerconaServerMySQLList{})
}
