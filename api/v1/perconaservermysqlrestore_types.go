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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PerconaServerMySQLRestoreSpec defines the desired state of PerconaServerMySQLRestore
type PerconaServerMySQLRestoreSpec struct {
	ClusterName  string                          `json:"clusterName"`
	BackupName   string                          `json:"backupName,omitempty"`
	BackupSource *PerconaServerMySQLBackupStatus `json:"backupSource,omitempty"`
}

type RestoreState string

const (
	RestoreNew       RestoreState = ""
	RestoreStarting  RestoreState = "Starting"
	RestoreRunning   RestoreState = "Running"
	RestoreFailed    RestoreState = "Failed"
	RestoreError     RestoreState = "Error"
	RestoreSucceeded RestoreState = "Succeeded"
)

// PerconaServerMySQLRestoreStatus defines the observed state of PerconaServerMySQLRestore
type PerconaServerMySQLRestoreStatus struct { // INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	State       RestoreState `json:"state,omitempty"`
	StateDesc   string       `json:"stateDescription,omitempty"`
	CompletedAt *metav1.Time `json:"completed,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:resource:shortName=ps-restore
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=".status.state"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// PerconaServerMySQLRestore is the Schema for the perconaservermysqlrestores API
type PerconaServerMySQLRestore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PerconaServerMySQLRestoreSpec   `json:"spec,omitempty"`
	Status PerconaServerMySQLRestoreStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PerconaServerMySQLRestoreList contains a list of PerconaServerMySQLRestore
type PerconaServerMySQLRestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PerconaServerMySQLRestore `json:"items"`
}

// Registers PerconaServerMySQLRestore types with the SchemeBuilder.
func init() {
	SchemeBuilder.Register(&PerconaServerMySQLRestore{}, &PerconaServerMySQLRestoreList{})
}

// Labels returns a standardized set of labels for the PerconaServerMySQLRestore custom resource.
func (cr *PerconaServerMySQLRestore) Labels(name, component string) map[string]string {
	return naming.Labels(name, cr.Name, "percona-server-restore", component)
}
