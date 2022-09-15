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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// PerconaServerMySQLBackupSpec defines the desired state of PerconaServerMySQLBackup
type PerconaServerMySQLBackupSpec struct {
	ClusterName string `json:"clusterName"`
	StorageName string `json:"storageName"`
}

type BackupState string

const (
	BackupNew       BackupState = ""
	BackupStarting  BackupState = "Starting"
	BackupRunning   BackupState = "Running"
	BackupFailed    BackupState = "Failed"
	BackupSucceeded BackupState = "Succeeded"
)

// PerconaServerMySQLBackupStatus defines the observed state of PerconaServerMySQLBackup
type PerconaServerMySQLBackupStatus struct {
	State         BackupState        `json:"state,omitempty"`
	Destination   string             `json:"destination,omitempty"`
	Storage       *BackupStorageSpec `json:"storage,omitempty"`
	CompletedAt   *metav1.Time       `json:"completed,omitempty"`
	Image         string             `json:"image,omitempty"`
	SSLSecretName string             `json:"sslSecretName,omitempty"`
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

func init() {
	SchemeBuilder.Register(&PerconaServerMySQLBackup{}, &PerconaServerMySQLBackupList{})
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

type SidecarBackupConfig struct {
	Destination string            `json:"destination"`
	Type        BackupStorageType `json:"type"`
	VerifyTLS   bool              `json:"verifyTLS,omitempty"`
	S3          struct {
		Bucket       string `json:"bucket"`
		Region       string `json:"region,omitempty"`
		EndpointURL  string `json:"endpointUrl,omitempty"`
		StorageClass string `json:"storageClass,omitempty"`
		AccessKey    string `json:"accessKey,omitempty"`
		SecretKey    string `json:"secretKey,omitempty"`
	} `json:"s3,omitempty"`
	GCS struct {
		Bucket       string `json:"bucket"`
		EndpointURL  string `json:"endpointUrl,omitempty"`
		StorageClass string `json:"storageClass,omitempty"`
		AccessKey    string `json:"accessKey,omitempty"`
		SecretKey    string `json:"secretKey,omitempty"`
	} `json:"gcs,omitempty"`
	Azure struct {
		ContainerName  string `json:"containerName"`
		EndpointURL    string `json:"endpointUrl,omitempty"`
		StorageClass   string `json:"storageClass,omitempty"`
		StorageAccount string `json:"storageAccount,omitempty"`
		AccessKey      string `json:"accessKey,omitempty"`
	} `json:"azure,omitempty"`
}
