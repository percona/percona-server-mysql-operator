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

package v1

import (
	"path"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

// PerconaServerMySQLBackupSpec defines the desired state of PerconaServerMySQLBackup
type PerconaServerMySQLBackupSpec struct {
	// +kubebuilder:validation:Enum=full;incremental
	// +kubebuilder:default:=full
	Type             BackupType              `json:"type,omitempty"`
	ClusterName      string                  `json:"clusterName"`
	StorageName      string                  `json:"storageName"`
	SourcePod        string                  `json:"sourcePod,omitempty"`
	ContainerOptions *BackupContainerOptions `json:"containerOptions,omitempty"`
}

type BackupState string

const (
	BackupNew       BackupState = ""
	BackupStarting  BackupState = "Starting"
	BackupRunning   BackupState = "Running"
	BackupSucceeded BackupState = "Succeeded"

	// Used for backups that failed to start at all
	BackupError BackupState = "Error"
	// Used for backups that started but failed
	BackupFailed BackupState = "Failed"
)

type BackupType string

const (
	BackupTypeFull        BackupType = "full"
	BackupTypeIncremental BackupType = "incremental"
)

// PerconaServerMySQLBackupStatus defines the observed state of PerconaServerMySQLBackup
type PerconaServerMySQLBackupStatus struct {
	Type         BackupType         `json:"type,omitempty"`
	State        BackupState        `json:"state,omitempty"`
	StateDesc    string             `json:"stateDescription,omitempty"`
	Destination  BackupDestination  `json:"destination,omitempty"`
	Storage      *BackupStorageSpec `json:"storage,omitempty"`
	CompletedAt  *metav1.Time       `json:"completed,omitempty"`
	Image        string             `json:"image,omitempty"`
	BackupSource string             `json:"backupSource,omitempty"`
	ToLsn        *string            `json:"toLsn,omitempty"`
}

const (
	AzureBlobStoragePrefix string = ""
	AwsBlobStoragePrefix   string = "s3://"
	GCSStoragePrefix       string = "gs://"
)

type BackupDestination string

func (dest *BackupDestination) set(value string) {
	if dest == nil {
		return
	}
	*dest = BackupDestination(value)
}

func (dest *BackupDestination) IsIncremental() bool {
	return strings.Contains(dest.String(), ".incr")
}

// IncrementalBaseDestination returns the full destination of the base (full) backup for an incremental backup.
// For example, given "s3://bucket/prefix/weekly-full-1.incr/2026-03-17T000000",
// it returns "s3://bucket/prefix/weekly-full-1".
// Returns the destination unchanged if it's not incremental.
func (dest *BackupDestination) IncrementalBaseDestination() BackupDestination {
	s := dest.String()
	idx := strings.Index(s, ".incr")
	if idx == -1 {
		return *dest
	}
	return BackupDestination(s[:idx])
}

// IncrementalsDir returns the ".incr/" directory prefix used to list all incremental backups
// for a given base backup. For example, given "s3://bucket/prefix/weekly-full-1.incr/2026-03-17T000000",
// it returns "s3://bucket/prefix/weekly-full-1.incr/".
// Returns empty string if the destination is not incremental.
func (dest *BackupDestination) IncrementalsDir() string {
	s := dest.String()
	idx := strings.Index(s, ".incr")
	if idx == -1 {
		return ""
	}
	return s[:idx] + ".incr/"
}

func (dest *BackupDestination) SetGCSDestination(bucket, backupName string) {
	dest.set(GCSStoragePrefix + bucket + "/" + backupName)
}

func (dest *BackupDestination) SetS3Destination(bucket, backupName string) {
	dest.set(AwsBlobStoragePrefix + bucket + "/" + backupName)
}

func (dest *BackupDestination) SetAzureDestination(container, backupName string) {
	dest.set(AzureBlobStoragePrefix + container + "/" + backupName)
}

func (dest *BackupDestination) String() string {
	if dest == nil {
		return ""
	}
	return string(*dest)
}

func (dest *BackupDestination) StorageTypePrefix() string {
	for _, p := range []string{AwsBlobStoragePrefix, GCSStoragePrefix} {
		if strings.HasPrefix(dest.String(), p) {
			return p
		}
	}
	return AzureBlobStoragePrefix
}

func (dest *BackupDestination) BucketAndPrefix() (string, string) {
	d := strings.TrimPrefix(dest.String(), dest.StorageTypePrefix())
	bucket, left, _ := strings.Cut(d, "/")

	spl := strings.Split(left, "/")
	prefix := ""
	if len(spl) > 1 {
		prefix = path.Join(spl[:len(spl)-1]...)
		prefix = strings.TrimSuffix(prefix, "/")
		prefix += "/"
	}
	return bucket, prefix
}

func (dest *BackupDestination) PathWithoutBucket() string {
	_, prefix := dest.BucketAndPrefix()
	return path.Join(prefix, dest.BackupName())
}

func (dest *BackupDestination) BackupName() string {
	bucket, prefix := dest.BucketAndPrefix()
	backupName := strings.TrimPrefix(dest.String(), dest.StorageTypePrefix()+path.Join(bucket, prefix))
	backupName = strings.TrimPrefix(backupName, "/")
	return backupName
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=".status.type"
// +kubebuilder:printcolumn:name="Storage",type=string,JSONPath=".spec.storageName"
// +kubebuilder:printcolumn:name="Destination",type=string,JSONPath=".status.destination"
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=".status.state"
// +kubebuilder:printcolumn:name="Completed",type="date",JSONPath=".status.completed"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:resource:shortName=ps-backup;ps-backups
// PerconaServerMySQLBackup is the Schema for the perconaservermysqlbackups API
type PerconaServerMySQLBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PerconaServerMySQLBackupSpec   `json:"spec,omitempty"`
	Status PerconaServerMySQLBackupStatus `json:"status,omitempty"`
}

func (b *PerconaServerMySQLBackup) GetContainerOptions(storage *BackupStorageSpec) *BackupContainerOptions {
	if c := b.Spec.ContainerOptions; c != nil {
		return c
	}
	if storage != nil && storage.ContainerOptions != nil {
		return storage.ContainerOptions
	}
	return nil
}

//+kubebuilder:object:root=true

// PerconaServerMySQLBackupList contains a list of PerconaServerMySQLBackup
type PerconaServerMySQLBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PerconaServerMySQLBackup `json:"items"`
}

// Initializes the scheme with PerconaServerMySQLBackup types.
func init() {
	SchemeBuilder.Register(&PerconaServerMySQLBackup{}, &PerconaServerMySQLBackupList{})
}

// Labels returns a standardized set of labels for the PerconaServerMySQLBackup custom resource.
func (b *PerconaServerMySQLBackup) Labels(name, component string) map[string]string {
	return naming.Labels(name, b.Name, "percona-server-backup", component)
}

// Hash returns FNV hash of the PerconaServerMySQLBackup UID
func (b *PerconaServerMySQLBackup) Hash() string {
	hash := FNVHash([]byte(string(b.UID)))

	// We use only first 7 digits to give a space for pod number which is
	// appended to all server ids. If we don't do this, it can cause a
	// int32 overflow.
	// P.S max value is 4294967295
	if len(hash) > 7 {
		hash = hash[:7]
	}

	return hash
}

func (s *PerconaServerMySQLBackupStatus) Equals(other *PerconaServerMySQLBackupStatus) bool {
	return s.Type == other.Type &&
		s.State == other.State &&
		s.StateDesc == other.StateDesc &&
		s.Destination == other.Destination &&
		s.Image == other.Image &&
		s.BackupSource == other.BackupSource &&
		ptr.Deref(s.ToLsn, "") == ptr.Deref(other.ToLsn, "")
}
