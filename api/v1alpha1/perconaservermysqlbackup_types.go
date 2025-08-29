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
	"path"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

// PerconaServerMySQLBackupSpec defines the desired state of PerconaServerMySQLBackup
type PerconaServerMySQLBackupSpec struct {
	SourceHost       string                  `json:"sourceHost,omitempty"`
	ClusterName      string                  `json:"clusterName"`
	StorageName      string                  `json:"storageName"`
	ContainerOptions *BackupContainerOptions `json:"containerOptions,omitempty"`
}

type BackupState string

const (
	BackupNew       BackupState = ""
	BackupStarting  BackupState = "Starting"
	BackupRunning   BackupState = "Running"
	BackupFailed    BackupState = "Failed"
	BackupError     BackupState = "Error"
	BackupSucceeded BackupState = "Succeeded"
)

// PerconaServerMySQLBackupStatus defines the observed state of PerconaServerMySQLBackup
type PerconaServerMySQLBackupStatus struct {
	State       BackupState        `json:"state,omitempty"`
	StateDesc   string             `json:"stateDescription,omitempty"`
	Destination BackupDestination  `json:"destination,omitempty"`
	Storage     *BackupStorageSpec `json:"storage,omitempty"`
	CompletedAt *metav1.Time       `json:"completed,omitempty"`
	Image       string             `json:"image,omitempty"`
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
func (cr *PerconaServerMySQLBackup) Labels(name, component string) map[string]string {
	return naming.Labels(name, cr.Name, "percona-server-backup", component)
}

// Hash returns FNV hash of the PerconaServerMySQLBackup UID
func (cr *PerconaServerMySQLBackup) Hash() string {
	hash := FNVHash([]byte(string(cr.UID)))

	// We use only first 7 digits to give a space for pod number which is
	// appended to all server ids. If we don't do this, it can cause a
	// int32 overflow.
	// P.S max value is 4294967295
	if len(hash) > 7 {
		hash = hash[:7]
	}

	return hash
}
