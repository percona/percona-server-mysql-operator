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
	"context"
	"fmt"
	"hash/fnv"
	"path"
	"regexp"
	"strings"

	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	"github.com/pkg/errors"
	"github.com/robfig/cron/v3"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PerconaServerMySQLSpec defines the desired state of PerconaServerMySQL
type PerconaServerMySQLSpec struct {
	CRVersion         string                               `json:"crVersion,omitempty"`
	Pause             bool                                 `json:"pause,omitempty"`
	SecretsName       string                               `json:"secretsName,omitempty"`
	SSLSecretName     string                               `json:"sslSecretName,omitempty"`
	Unsafe            UnsafeFlags                          `json:"unsafeFlags,omitempty"`
	InitImage         string                               `json:"initImage,omitempty"`
	IgnoreAnnotations []string                             `json:"ignoreAnnotations,omitempty"`
	IgnoreLabels      []string                             `json:"ignoreLabels,omitempty"`
	MySQL             MySQLSpec                            `json:"mysql,omitempty"`
	Orchestrator      OrchestratorSpec                     `json:"orchestrator,omitempty"`
	PMM               *PMMSpec                             `json:"pmm,omitempty"`
	Backup            *BackupSpec                          `json:"backup,omitempty"`
	Proxy             ProxySpec                            `json:"proxy,omitempty"`
	TLS               *TLSSpec                             `json:"tls,omitempty"`
	Toolkit           *ToolkitSpec                         `json:"toolkit,omitempty"`
	UpgradeOptions    UpgradeOptions                       `json:"upgradeOptions,omitempty"`
	UpdateStrategy    appsv1.StatefulSetUpdateStrategyType `json:"updateStrategy,omitempty"`
}

type UnsafeFlags struct {
	// MySQLSize allows to set MySQL size to a value less than the minimum safe size or higher than the maximum safe size.
	MySQLSize bool `json:"mysqlSize,omitempty"`

	// Proxy allows to disable proxy.
	Proxy bool `json:"proxy,omitempty"`
	// ProxySize allows to set proxy (HAProxy / Router) size to a value less than the minimum safe size.
	ProxySize bool `json:"proxySize,omitempty"`

	// Orchestrator allows to disable Orchestrator.
	Orchestrator bool `json:"orchestrator,omitempty"`
	// OrchestratorSize allows to set Orchestrator size to a value less than the minimum safe size.
	OrchestratorSize bool `json:"orchestratorSize,omitempty"`
}

type TLSSpec struct {
	SANs       []string                `json:"SANs,omitempty"`
	IssuerConf *cmmeta.ObjectReference `json:"issuerConf,omitempty"`
}

type ClusterType string

const (
	ClusterTypeGR    ClusterType = "group-replication"
	ClusterTypeAsync ClusterType = "async"
	MinSafeProxySize             = 2
	MinSafeGRSize                = 3
	MaxSafeGRSize                = 9
	MinSafeAsyncSize             = 2
)

// Checks if the provided ClusterType is valid.
func (t ClusterType) isValid() bool {
	switch ClusterType(t) {
	case ClusterTypeGR, ClusterTypeAsync:
		return true
	}

	return false
}

type MySQLSpec struct {
	ClusterType  ClusterType            `json:"clusterType,omitempty"`
	Expose       ServiceExposeTogglable `json:"expose,omitempty"`
	AutoRecovery bool                   `json:"autoRecovery,omitempty"`

	Sidecars       []corev1.Container `json:"sidecars,omitempty"`
	SidecarVolumes []corev1.Volume    `json:"sidecarVolumes,omitempty"`
	SidecarPVCs    []SidecarPVC       `json:"sidecarPVCs,omitempty"`

	PodSpec `json:",inline"`
}

// Checks if the MySQL cluster type is asynchronous.
func (m MySQLSpec) IsAsync() bool {
	return m.ClusterType == ClusterTypeAsync
}

// Checks if the MySQL cluster type is Group Replication (GR).
func (m MySQLSpec) IsGR() bool {
	return m.ClusterType == ClusterTypeGR
}

type SidecarPVC struct {
	Name string `json:"name"`

	Spec corev1.PersistentVolumeClaimSpec `json:"spec"`
}

type OrchestratorSpec struct {
	Enabled bool          `json:"enabled,omitempty"`
	Expose  ServiceExpose `json:"expose,omitempty"`

	PodSpec `json:",inline"`
}

type ContainerSpec struct {
	Image            string                        `json:"image"`
	ImagePullPolicy  corev1.PullPolicy             `json:"imagePullPolicy,omitempty"`
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	Resources        corev1.ResourceRequirements   `json:"resources,omitempty"`

	StartupProbe   corev1.Probe `json:"startupProbe,omitempty"`
	ReadinessProbe corev1.Probe `json:"readinessProbe,omitempty"`
	LivenessProbe  corev1.Probe `json:"livenessProbe,omitempty"`

	ContainerSecurityContext *corev1.SecurityContext `json:"containerSecurityContext,omitempty"`

	Env     []corev1.EnvVar        `json:"env,omitempty"`
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`
}

type PodSpec struct {
	Size        int32             `json:"size,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	VolumeSpec  *VolumeSpec       `json:"volumeSpec,omitempty"`
	InitImage   string            `json:"initImage,omitempty"`

	Affinity                      *PodAffinity                      `json:"affinity,omitempty"`
	TopologySpreadConstraints     []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	NodeSelector                  map[string]string                 `json:"nodeSelector,omitempty"`
	Tolerations                   []corev1.Toleration               `json:"tolerations,omitempty"`
	PriorityClassName             string                            `json:"priorityClassName,omitempty"`
	TerminationGracePeriodSeconds *int64                            `json:"gracePeriod,omitempty"`
	SchedulerName                 string                            `json:"schedulerName,omitempty"`
	RuntimeClassName              *string                           `json:"runtimeClassName,omitempty"`

	PodSecurityContext *corev1.PodSecurityContext `json:"podSecurityContext,omitempty"`
	ServiceAccountName string                     `json:"serviceAccountName,omitempty"`

	Configuration string `json:"configuration,omitempty"`

	ContainerSpec `json:",inline"`
}

// Retrieves the initialization image for the pod.
func (s *PodSpec) GetInitImage() string {
	return s.InitImage
}

type PMMSpec struct {
	Enabled                  bool                        `json:"enabled,omitempty"`
	Image                    string                      `json:"image"`
	ServerHost               string                      `json:"serverHost,omitempty"`
	ServerUser               string                      `json:"serverUser,omitempty"`
	Resources                corev1.ResourceRequirements `json:"resources,omitempty"`
	ContainerSecurityContext *corev1.SecurityContext     `json:"containerSecurityContext,omitempty"`
	ImagePullPolicy          corev1.PullPolicy           `json:"imagePullPolicy,omitempty"`
	RuntimeClassName         *string                     `json:"runtimeClassName,omitempty"`
}

type BackupSpec struct {
	Enabled                  bool                          `json:"enabled,omitempty"`
	Image                    string                        `json:"image"`
	InitImage                string                        `json:"initImage,omitempty"`
	ImagePullSecrets         []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	ImagePullPolicy          corev1.PullPolicy             `json:"imagePullPolicy,omitempty"`
	ServiceAccountName       string                        `json:"serviceAccountName,omitempty"`
	ContainerSecurityContext *corev1.SecurityContext       `json:"containerSecurityContext,omitempty"`
	Resources                corev1.ResourceRequirements   `json:"resources,omitempty"`
	Storages                 map[string]*BackupStorageSpec `json:"storages,omitempty"`
	BackoffLimit             *int32                        `json:"backoffLimit,omitempty"`
	PiTR                     PiTRSpec                      `json:"pitr,omitempty"`
	Schedule                 []BackupSchedule              `json:"schedule,omitempty"`
}

type BackupSchedule struct {
	// +kubebuilder:validation:Required
	Name string `json:"name,omitempty"`
	// +kubebuilder:validation:Required
	Schedule string `json:"schedule,omitempty"`
	Keep     int    `json:"keep,omitempty"`
	// +kubebuilder:validation:Required
	StorageName string `json:"storageName,omitempty"`
}

// Retrieves the initialization image for the backup.
func (s *BackupSpec) GetInitImage() string {
	return s.InitImage
}

type BackupStorageType string

const (
	BackupStorageFilesystem BackupStorageType = "filesystem"
	BackupStorageS3         BackupStorageType = "s3"
	BackupStorageGCS        BackupStorageType = "gcs"
	BackupStorageAzure      BackupStorageType = "azure"
)

type BackupStorageSpec struct {
	Type                      BackupStorageType                 `json:"type"`
	Volume                    *VolumeSpec                       `json:"volumeSpec,omitempty"`
	S3                        *BackupStorageS3Spec              `json:"s3,omitempty"`
	GCS                       *BackupStorageGCSSpec             `json:"gcs,omitempty"`
	Azure                     *BackupStorageAzureSpec           `json:"azure,omitempty"`
	NodeSelector              map[string]string                 `json:"nodeSelector,omitempty"`
	Resources                 corev1.ResourceRequirements       `json:"resources,omitempty"`
	Affinity                  *corev1.Affinity                  `json:"affinity,omitempty"`
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	Tolerations               []corev1.Toleration               `json:"tolerations,omitempty"`
	Annotations               map[string]string                 `json:"annotations,omitempty"`
	Labels                    map[string]string                 `json:"labels,omitempty"`
	SchedulerName             string                            `json:"schedulerName,omitempty"`
	PriorityClassName         string                            `json:"priorityClassName,omitempty"`
	PodSecurityContext        *corev1.PodSecurityContext        `json:"podSecurityContext,omitempty"`
	ContainerSecurityContext  *corev1.SecurityContext           `json:"containerSecurityContext,omitempty"`
	RuntimeClassName          *string                           `json:"runtimeClassName,omitempty"`
	VerifyTLS                 *bool                             `json:"verifyTLS,omitempty"`
}

type BackupStorageS3Spec struct {
	Bucket            BucketWithPrefix `json:"bucket"`
	Prefix            string           `json:"prefix,omitempty"`
	CredentialsSecret string           `json:"credentialsSecret"`
	Region            string           `json:"region,omitempty"`
	EndpointURL       string           `json:"endpointUrl,omitempty"`
	StorageClass      string           `json:"storageClass,omitempty"`
}

// BucketAndPrefix returns bucket name and backup prefix from Bucket concatenated with Prefix.
// BackupStorageS3Spec.Bucket can contain backup path in format `<bucket-name>/<backup-prefix>`.
func (b *BackupStorageS3Spec) BucketAndPrefix() (string, string) {
	bucket := b.Bucket.bucket()
	prefix := b.Bucket.prefix()
	if b.Prefix != "" {
		prefix = path.Join(prefix, b.Prefix)
	}
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/")
		prefix += "/"
	}

	return bucket, prefix
}

// BucketWithPrefix contains a bucket name with or without a prefix in a format <bucket>/<prefix>
type BucketWithPrefix string

// Extracts the bucket name from a combined bucket with prefix string.
func (b BucketWithPrefix) bucket() string {
	bucket, _, _ := strings.Cut(string(b), "/")
	return bucket
}

// Extracts the prefix from a combined bucket with prefix string.
func (b BucketWithPrefix) prefix() string {
	_, prefix, _ := strings.Cut(string(b), "/")
	return prefix
}

type BackupStorageGCSSpec struct {
	Bucket            BucketWithPrefix `json:"bucket"`
	Prefix            string           `json:"prefix,omitempty"`
	CredentialsSecret string           `json:"credentialsSecret"`
	EndpointURL       string           `json:"endpointUrl,omitempty"`

	// STANDARD, NEARLINE, COLDLINE, ARCHIVE
	StorageClass string `json:"storageClass,omitempty"`
}

// BucketAndPrefix returns bucket name and backup prefix from Bucket concatenated with Prefix.
// BackupStorageGCSSpec.Bucket can contain backup path in format `<bucket-name>/<backup-prefix>`.
func (b *BackupStorageGCSSpec) BucketAndPrefix() (string, string) {
	bucket := b.Bucket.bucket()
	prefix := b.Bucket.prefix()
	if b.Prefix != "" {
		prefix = path.Join(prefix, b.Prefix)
	}
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/")
		prefix += "/"
	}

	return bucket, prefix
}

type BackupStorageAzureSpec struct {
	// A container name is a valid DNS name that conforms to the Azure naming rules.
	ContainerName BucketWithPrefix `json:"containerName"`

	// A prefix is a sub-folder to the backups inside the container
	Prefix string `json:"prefix,omitempty"`

	// A generated key that can be used to authorize access to data in your account using the Shared Key authorization.
	CredentialsSecret string `json:"credentialsSecret"`

	// The endpoint allows clients to securely access data
	EndpointURL string `json:"endpointUrl,omitempty"`

	// Hot (Frequently accessed or modified data), Cool (Infrequently accessed or modified data), Archive (Rarely accessed or modified data)
	StorageClass string `json:"storageClass,omitempty"`
}

// ContainerAndPrefix returns container name from ContainerName and backup prefix from ContainerName concatenated with Prefix.
// BackupStorageAzureSpec.ContainerName can contain backup path in format `<container-name>/<backup-prefix>`.
func (b *BackupStorageAzureSpec) ContainerAndPrefix() (string, string) {
	container := b.ContainerName.bucket()
	prefix := b.ContainerName.prefix()
	if b.Prefix != "" {
		prefix = path.Join(prefix, b.Prefix)
	}
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/")
		prefix += "/"
	}

	return container, prefix
}

type PiTRSpec struct {
	Enabled bool `json:"enabled,omitempty"`

	BinlogServer BinlogServerSpec `json:"binlogServer,omitempty"`
}

type BinlogServerStorageSpec struct {
	S3 *BackupStorageS3Spec `json:"s3,omitempty"`
}

type BinlogServerSpec struct {
	Storage BinlogServerStorageSpec `json:"storage"`

	// The number of seconds the MySQL client library will wait to establish a connection with a remote host
	ConnectTimeout int32 `json:"connectTimeout"`
	// The number of seconds the MySQL client library will wait to read data from a remote server.
	ReadTimeout int32 `json:"readTimeout"`
	// The number of seconds the MySQL client library will wait to write data to a remote server.
	WriteTimeout int32 `json:"writeTimeout"`
	// Specifies the server ID that the utility will be using when connecting to a remote MySQL server
	ServerID int32 `json:"serverId"`
	// The number of seconds the utility will spend in disconnected mode between reconnection attempts.
	IdleTime int32 `json:"idleTime"`

	PodSpec `json:",inline"`
}

type ProxySpec struct {
	Router  *MySQLRouterSpec `json:"router,omitempty"`
	HAProxy *HAProxySpec     `json:"haproxy,omitempty"`
}

type MySQLRouterSpec struct {
	Enabled bool `json:"enabled,omitempty"`

	Expose ServiceExpose `json:"expose,omitempty"`

	PodSpec `json:",inline"`
}

type ToolkitSpec struct {
	ContainerSpec `json:",inline"`
}

type HAProxySpec struct {
	Enabled bool `json:"enabled,omitempty"`

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
	Type                     corev1.ServiceType                       `json:"type,omitempty"`
	LoadBalancerIP           string                                   `json:"loadBalancerIP,omitempty"`
	LoadBalancerSourceRanges []string                                 `json:"loadBalancerSourceRanges,omitempty"`
	Annotations              map[string]string                        `json:"annotations,omitempty"`
	Labels                   map[string]string                        `json:"labels,omitempty"`
	InternalTrafficPolicy    *corev1.ServiceInternalTrafficPolicyType `json:"internalTrafficPolicy,omitempty"`
	ExternalTrafficPolicy    corev1.ServiceExternalTrafficPolicyType  `json:"externalTrafficPolicy,omitempty"`
}

// Determines if both annotations and labels of the service expose are empty.
func (e *ServiceExpose) SaveOldMeta() bool {
	return len(e.Annotations) == 0 && len(e.Labels) == 0
}

type ServiceExposeTogglable struct {
	Enabled bool `json:"enabled,omitempty"`

	ServiceExpose `json:",inline"`
}

type StatefulAppState string

func (s StatefulAppState) String() string {
	return cases.Title(language.English).String(string(s))
}

const (
	StateInitializing StatefulAppState = "initializing"
	StateStopping     StatefulAppState = "stopping"
	StatePaused       StatefulAppState = "paused"
	StateReady        StatefulAppState = "ready"
	StateError        StatefulAppState = "error"
)

type StatefulAppStatus struct {
	Size    int32            `json:"size,omitempty"`
	Ready   int32            `json:"ready,omitempty"`
	State   StatefulAppState `json:"state,omitempty"`
	Version string           `json:"version,omitempty"`
}

// PerconaServerMySQLStatus defines the observed state of PerconaServerMySQL
type PerconaServerMySQLStatus struct { // INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	MySQL          StatefulAppStatus  `json:"mysql,omitempty"`
	Orchestrator   StatefulAppStatus  `json:"orchestrator,omitempty"`
	HAProxy        StatefulAppStatus  `json:"haproxy,omitempty"`
	Router         StatefulAppStatus  `json:"router,omitempty"`
	State          StatefulAppState   `json:"state,omitempty"`
	BackupVersion  string             `json:"backupVersion,omitempty"`
	PMMVersion     string             `json:"pmmVersion,omitempty"`
	ToolkitVersion string             `json:"toolkitVersion,omitempty"`
	Conditions     []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	Host string `json:"host"`
}

const ConditionInnoDBClusterBootstrapped string = "InnoDBClusterBootstrapped"

// PerconaServerMySQL is the Schema for the perconaservermysqls API
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Replication",type=string,JSONPath=".spec.mysql.clusterType"
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=".status.host"
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=".status.state"
// +kubebuilder:printcolumn:name="MySQL",type=string,JSONPath=".status.mysql.ready"
// +kubebuilder:printcolumn:name="Orchestrator",type=string,JSONPath=".status.orchestrator.ready"
// +kubebuilder:printcolumn:name="HAProxy",type=string,JSONPath=".status.haproxy.ready"
// +kubebuilder:printcolumn:name="Router",type=string,JSONPath=".status.router.ready"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:resource:shortName=ps
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
	UserHeartbeat    SystemUser = "heartbeat"
	UserMonitor      SystemUser = "monitor"
	UserOperator     SystemUser = "operator"
	UserOrchestrator SystemUser = "orchestrator"
	UserPMMServerKey SystemUser = "pmmserverkey"
	UserReplication  SystemUser = "replication"
	UserRoot         SystemUser = "root"
	UserXtraBackup   SystemUser = "xtrabackup"
)

// MySQLSpec returns the MySQL specification from the PerconaServerMySQL custom resource.
func (cr *PerconaServerMySQL) MySQLSpec() *MySQLSpec {
	return &cr.Spec.MySQL
}

// PMMSpec returns the PMM specification from the PerconaServerMySQL custom resource.
func (cr *PerconaServerMySQL) PMMSpec() *PMMSpec {
	return cr.Spec.PMM
}

// OrchestratorSpec returns the Orchestrator specification from the PerconaServerMySQL custom resource.
func (cr *PerconaServerMySQL) OrchestratorSpec() *OrchestratorSpec {
	return &cr.Spec.Orchestrator
}

// SetVersion sets the CRVersion to the version value if it's not already set.
func (cr *PerconaServerMySQL) SetVersion() {
	if len(cr.Spec.CRVersion) > 0 {
		return
	}

	cr.Spec.CRVersion = version.Version
}

// CheckNSetDefaults validates and sets default values for the PerconaServerMySQL custom resource.
func (cr *PerconaServerMySQL) CheckNSetDefaults(ctx context.Context, serverVersion *platform.ServerVersion) error {
	if len(cr.Spec.MySQL.ClusterType) == 0 {
		cr.Spec.MySQL.ClusterType = ClusterTypeAsync
	}

	if valid := cr.Spec.MySQL.ClusterType.isValid(); !valid {
		return errors.Errorf("%s is not a valid clusterType, valid options are %s and %s", cr.Spec.MySQL.ClusterType, ClusterTypeGR, ClusterTypeAsync)
	}

	cr.SetVersion()

	if cr.Spec.Backup == nil {
		cr.Spec.Backup = new(BackupSpec)
	}

	if len(cr.Spec.Backup.Image) == 0 {
		return errors.New("backup.image can't be empty")
	}

	scheduleNames := make(map[string]struct{}, len(cr.Spec.Backup.Schedule))
	for _, sch := range cr.Spec.Backup.Schedule {
		if _, ok := scheduleNames[sch.Name]; ok {
			return errors.Errorf("scheduled backups should have different names: %s name is used by multiple schedules", sch.Name)
		}
		scheduleNames[sch.Name] = struct{}{}
		_, ok := cr.Spec.Backup.Storages[sch.StorageName]
		if !ok {
			return errors.Errorf("storage %s doesn't exist", sch.StorageName)
		}
		if sch.Schedule != "" {
			_, err := cron.ParseStandard(sch.Schedule)
			if err != nil {
				return errors.Wrap(err, "invalid schedule format")
			}
		}
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
		cr.Spec.MySQL.StartupProbe.TimeoutSeconds = 12 * 60 * 60
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
		cr.Spec.MySQL.LivenessProbe.TimeoutSeconds = 10
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

	if cr.Spec.Proxy.Router == nil {
		cr.Spec.Proxy.Router = new(MySQLRouterSpec)
	}

	if cr.Spec.Proxy.Router.StartupProbe.InitialDelaySeconds == 0 {
		cr.Spec.Proxy.Router.StartupProbe.InitialDelaySeconds = 5
	}
	if cr.Spec.Proxy.Router.StartupProbe.PeriodSeconds == 0 {
		cr.Spec.Proxy.Router.StartupProbe.PeriodSeconds = 5
	}
	if cr.Spec.Proxy.Router.StartupProbe.FailureThreshold == 0 {
		cr.Spec.Proxy.Router.StartupProbe.FailureThreshold = 1
	}
	if cr.Spec.Proxy.Router.StartupProbe.SuccessThreshold == 0 {
		cr.Spec.Proxy.Router.StartupProbe.SuccessThreshold = 1
	}
	if cr.Spec.Proxy.Router.StartupProbe.TimeoutSeconds == 0 {
		cr.Spec.Proxy.Router.StartupProbe.TimeoutSeconds = 3
	}

	if cr.Spec.Proxy.Router.ReadinessProbe.PeriodSeconds == 0 {
		cr.Spec.Proxy.Router.ReadinessProbe.PeriodSeconds = 5
	}
	if cr.Spec.Proxy.Router.ReadinessProbe.FailureThreshold == 0 {
		cr.Spec.Proxy.Router.ReadinessProbe.FailureThreshold = 3
	}
	if cr.Spec.Proxy.Router.ReadinessProbe.SuccessThreshold == 0 {
		cr.Spec.Proxy.Router.ReadinessProbe.SuccessThreshold = 1
	}
	if cr.Spec.Proxy.Router.ReadinessProbe.TimeoutSeconds == 0 {
		cr.Spec.Proxy.Router.ReadinessProbe.TimeoutSeconds = 3
	}

	if cr.Spec.Proxy.HAProxy == nil {
		cr.Spec.Proxy.HAProxy = new(HAProxySpec)
	}

	if cr.Spec.Proxy.HAProxy.LivenessProbe.PeriodSeconds == 0 {
		cr.Spec.Proxy.HAProxy.LivenessProbe.PeriodSeconds = 5
	}
	if cr.Spec.Proxy.HAProxy.LivenessProbe.FailureThreshold == 0 {
		cr.Spec.Proxy.HAProxy.LivenessProbe.FailureThreshold = 3
	}
	if cr.Spec.Proxy.HAProxy.LivenessProbe.SuccessThreshold == 0 {
		cr.Spec.Proxy.HAProxy.LivenessProbe.SuccessThreshold = 1
	}
	if cr.Spec.Proxy.HAProxy.LivenessProbe.TimeoutSeconds == 0 {
		cr.Spec.Proxy.HAProxy.LivenessProbe.TimeoutSeconds = 3
	}

	if cr.Spec.Proxy.HAProxy.ReadinessProbe.PeriodSeconds == 0 {
		cr.Spec.Proxy.HAProxy.ReadinessProbe.PeriodSeconds = 5
	}
	if cr.Spec.Proxy.HAProxy.ReadinessProbe.FailureThreshold == 0 {
		cr.Spec.Proxy.HAProxy.ReadinessProbe.FailureThreshold = 3
	}
	if cr.Spec.Proxy.HAProxy.ReadinessProbe.SuccessThreshold == 0 {
		cr.Spec.Proxy.HAProxy.ReadinessProbe.SuccessThreshold = 1
	}
	if cr.Spec.Proxy.HAProxy.ReadinessProbe.TimeoutSeconds == 0 {
		cr.Spec.Proxy.HAProxy.ReadinessProbe.TimeoutSeconds = 3
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

	if cr.Spec.Orchestrator.ServiceAccountName == "" {
		cr.Spec.Orchestrator.ServiceAccountName = "percona-server-mysql-operator-orchestrator"
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

	if cr.Spec.MySQL.Size == 1 {
		cr.Spec.UpdateStrategy = appsv1.RollingUpdateStatefulSetStrategyType
	}

	if cr.Spec.MySQL.ClusterType == ClusterTypeGR {
		if !cr.Spec.Proxy.Router.Enabled && !cr.Spec.Proxy.HAProxy.Enabled && !cr.Spec.Unsafe.Proxy {
			return errors.New("MySQL Router or HAProxy must be enabled for Group Replication. Enable spec.unsafeFlags.proxy to bypass this check")
		}

		if cr.RouterEnabled() && !cr.Spec.Unsafe.ProxySize {
			if cr.Spec.Proxy.Router.Size < MinSafeProxySize {
				return errors.Errorf("Router size should be %d or greater. Enable spec.unsafeFlags.proxySize to set a lower size", MinSafeProxySize)
			}
		}

		if !cr.Spec.Unsafe.MySQLSize {
			if cr.Spec.MySQL.Size < MinSafeGRSize {
				return errors.Errorf("MySQL size should be %d or greater for Group Replication. Enable spec.unsafeFlags.mysqlSize to set a lower size", MinSafeGRSize)
			}

			if cr.Spec.MySQL.Size > MaxSafeGRSize {
				return errors.Errorf("MySQL size should be %d or lower for Group Replication. Enable spec.unsafeFlags.mysqlSize to set a higher size", MaxSafeGRSize)
			}

			if cr.Spec.MySQL.Size%2 == 0 {
				return errors.New("MySQL size should be an odd number for Group Replication. Enable spec.unsafeFlags.mysqlSize to set an even number")
			}
		}
	}

	if cr.Spec.MySQL.ClusterType == ClusterTypeAsync {
		if !cr.Spec.Unsafe.MySQLSize && cr.Spec.MySQL.Size < MinSafeAsyncSize {
			return errors.Errorf("MySQL size should be %d or greater for asynchronous replication. Enable spec.unsafeFlags.mysqlSize to set a lower size", MinSafeAsyncSize)
		}

		if !cr.Spec.Unsafe.Orchestrator && !cr.Spec.Orchestrator.Enabled {
			return errors.New("Orchestrator must be enabled for asynchronous replication. Enable spec.unsafeFlags.orchestrator to bypass this check")
		}

		if oSize := int(cr.Spec.Orchestrator.Size); cr.OrchestratorEnabled() && (oSize < 3 || oSize%2 == 0) && oSize != 0 && !cr.Spec.Unsafe.OrchestratorSize {
			return errors.New("Orchestrator size must be 3 or greater and an odd number for raft setup. Enable spec.unsafeFlags.orchestratorSize to bypass this check")
		}

		if !cr.Spec.Orchestrator.Enabled && cr.Spec.UpdateStrategy == SmartUpdateStatefulSetStrategyType {
			return errors.Errorf("Orchestrator must be enabled to use SmartUpdate. Either enable Orchestrator or set spec.updateStrategy to %s", appsv1.RollingUpdateStatefulSetStrategyType)
		}

		if !cr.Spec.Unsafe.Proxy && !cr.HAProxyEnabled() {
			return errors.New("HAProxy must be enabled for asynchronous replication. Enable spec.unsafeFlags.proxy to bypass this check")
		}

		if cr.RouterEnabled() {
			return errors.New("MySQL Router can't be enabled for asynchronous replication")
		}
	}

	if cr.RouterEnabled() && cr.HAProxyEnabled() {
		return errors.New("MySQL Router and HAProxy can't be enabled at the same time")
	}

	if cr.Spec.Proxy.HAProxy == nil {
		cr.Spec.Proxy.HAProxy = new(HAProxySpec)
	}

	if cr.HAProxyEnabled() && !cr.Spec.Unsafe.ProxySize {
		if cr.Spec.Proxy.HAProxy.Size < MinSafeProxySize {
			return errors.Errorf("HAProxy size should be %d or greater. Enable spec.unsafeFlags.proxySize to set a lower size", MinSafeProxySize)
		}
	}

	if cr.Spec.PMM == nil {
		cr.Spec.PMM = new(PMMSpec)
	}

	if cr.Spec.Toolkit == nil {
		cr.Spec.Toolkit = new(ToolkitSpec)
	}

	if cr.Spec.Pause {
		cr.Spec.MySQL.Size = 0
		cr.Spec.Orchestrator.Size = 0
		cr.Spec.Proxy.Router.Size = 0
		cr.Spec.Proxy.HAProxy.Size = 0
	}

	if cr.Spec.SSLSecretName == "" {
		cr.Spec.SSLSecretName = cr.Name + "-ssl"
	}

	return nil
}

const (
	BinVolumeName = "bin"
	BinVolumePath = "/opt/percona"
)

// reconcileVol validates and sets default values for a given VolumeSpec, ensuring it is properly defined.
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

// defaultPVCSpec sets default access mode for a PersistentVolumeClaimSpec if not already defined.
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

// GetAffinity derives an Affinity configuration based on the provided PodSpec's affinity settings and labels.
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

func (p *PodSpec) GetTopologySpreadConstraints(ls map[string]string) []corev1.TopologySpreadConstraint {
	tscs := make([]corev1.TopologySpreadConstraint, 0)

	for _, tsc := range p.TopologySpreadConstraints {
		if tsc.LabelSelector == nil && tsc.MatchLabelKeys == nil {
			tsc.LabelSelector = &metav1.LabelSelector{
				MatchLabels: ls,
			}
		}
		tscs = append(tscs, tsc)
	}
	return tscs
}

// Labels returns a standardized set of labels for the PerconaServerMySQL custom resource.
func (cr *PerconaServerMySQL) Labels() map[string]string {
	return map[string]string{
		naming.LabelName:      "percona-server",
		naming.LabelInstance:  cr.Name,
		naming.LabelManagedBy: "percona-server-operator",
		naming.LabelPartOf:    "percona-server",
	}
}

// ClusterHint generates a unique identifier for the PerconaServerMySQL
// cluster using its name and namespace.
func (cr *PerconaServerMySQL) ClusterHint() string {
	return fmt.Sprintf("%s.%s", cr.Name, cr.Namespace)
}

// GetClusterNameFromObject retrieves the cluster's name from the given client object's labels.
func GetClusterNameFromObject(obj client.Object) (string, error) {
	labels := obj.GetLabels()
	instance, ok := labels[naming.LabelInstance]
	if !ok {
		return "", errors.Errorf("label %s doesn't exist", naming.LabelInstance)
	}
	return instance, nil
}

// FNVHash computes a hash of the provided byte slice using the FNV-1a algorithm.
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

// InternalSecretName generates a name for the internal secret based on the PerconaServerMySQL name.
func (cr *PerconaServerMySQL) InternalSecretName() string {
	return "internal-" + cr.Name
}

// PMMEnabled checks if PMM is enabled and if the provided secret contains PMM-specific data.
func (cr *PerconaServerMySQL) PMMEnabled(secret *corev1.Secret) bool {
	if cr.Spec.PMM != nil && cr.Spec.PMM.Enabled && secret != nil && secret.Data != nil {
		return cr.Spec.PMM.HasSecret(secret)
	}
	return false
}

// HasSecret determines if the provided secret contains the necessary PMM server key.
func (pmm *PMMSpec) HasSecret(secret *corev1.Secret) bool {
	if secret.Data != nil {
		v, ok := secret.Data[string(UserPMMServerKey)]
		return ok && len(v) > 0
	}
	return false
}

// RouterEnabled checks if the router is enabled, considering the MySQL configuration.
func (cr *PerconaServerMySQL) RouterEnabled() bool {
	return cr.Spec.Proxy.Router != nil && cr.Spec.Proxy.Router.Enabled
}

// HAProxyEnabled verifies if HAProxy is enabled based on MySQL configuration and safety settings.
func (cr *PerconaServerMySQL) HAProxyEnabled() bool {
	return cr.Spec.Proxy.HAProxy != nil && cr.Spec.Proxy.HAProxy.Enabled
}

// OrchestratorEnabled determines if the orchestrator is enabled,
// considering the MySQL configuration.
func (cr *PerconaServerMySQL) OrchestratorEnabled() bool {
	if cr.MySQLSpec().IsGR() {
		return false
	}

	if cr.MySQLSpec().IsAsync() && !cr.Spec.Unsafe.Orchestrator {
		return true
	}

	return cr.Spec.Orchestrator.Enabled
}

var NonAlphaNumeric = regexp.MustCompile("[^a-zA-Z0-9_]+")

// Generates a cluster name by sanitizing the PerconaServerMySQL name.
func (cr *PerconaServerMySQL) InnoDBClusterName() string {
	return NonAlphaNumeric.ReplaceAllString(cr.Name, "")
}

// Registers PerconaServerMySQL types with the SchemeBuilder.
func init() {
	SchemeBuilder.Register(&PerconaServerMySQL{}, &PerconaServerMySQLList{})
}

// SmartUpdateStatefulSetStrategyType
const SmartUpdateStatefulSetStrategyType appsv1.StatefulSetUpdateStrategyType = "SmartUpdate"

type UpgradeOptions struct {
	VersionServiceEndpoint string `json:"versionServiceEndpoint,omitempty"`
	Apply                  string `json:"apply,omitempty"`
}

const (
	UpgradeStrategyDisabled    = "disabled"
	UpgradeStrategyNever       = "never"
	UpgradeStrategyRecommended = "recommended"
	UpgradeStrategyLatest      = "latest"
)
