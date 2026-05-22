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
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sretry "k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PerconaServerMySQLClusterSet is the Schema for the perconaservermysqlclustersets API
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:resource:shortName=ps-clusterset
type PerconaServerMySQLClusterSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PerconaServerMySQLClusterSetSpec   `json:"spec,omitempty"`
	Status PerconaServerMySQLClusterSetStatus `json:"status,omitempty"`
}

type PerconaServerMySQLClusterSetSpec struct {
	// +kubebuilder:validation:Required
	PrimaryCluster    string                   `json:"primaryCluster"`
	CredentialsSecret corev1.SecretKeySelector `json:"credentialsSecret"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=false
	AllowForcedFailover *bool               `json:"allowForcedFailover,omitempty"`
	Clusters            []ClusterSetCluster `json:"clusters"`

	MysqlShellRunner MysqlShellRunner `json:"mysqlShellRunner"`
}

type MysqlShellRunner struct {
	// +kubebuilder:validation:Required
	Image string `json:"image"`
}

type ClusterSetCluster struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems:=1
	Endpoints []ClusterSetClusterEndpoint `json:"endpoints"`
}

type ClusterSetClusterEndpoint struct {
	Host string `json:"host"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=3306
	Port *int32 `json:"port,omitempty"`
}

const (
	ConditionMySQLShellRunnerReady  string = "MySQLShellRunnerReady"
	ConditionClusterSetBootstrapped string = "ClusterSetBootstrapped"
)

type PerconaServerMySQLClusterSetStatus struct {
	PrimaryCluster string                             `json:"primaryCluster"`
	Conditions     []metav1.Condition                 `json:"conditions,omitempty"`
	Clusters       map[string]ClusterSetClusterStatus `json:"clusters,omitempty"`
}

// ClusterSetClusterStatus is the status of a single cluster in the cluster set.
// The shape of this object is derived from the output of `dba.getCluster().getClusterSet().status()` mysqlshell command.
type ClusterSetClusterStatus struct {
	ClusterRole    string `json:"clusterRole"`
	GlobalStatus   string `json:"globalStatus"`
	PrimaryCluster string `json:"primaryCluster"`
}

// +kubebuilder:object:root=true

// PerconaServerMySQLClusterSetList contains a list of PerconaServerMySQLClusterSet
type PerconaServerMySQLClusterSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PerconaServerMySQLClusterSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PerconaServerMySQLClusterSet{}, &PerconaServerMySQLClusterSetList{})
}

func (psc *PerconaServerMySQLClusterSet) PrimaryCluster() *ClusterSetCluster {
	primaryClusterName := psc.Spec.PrimaryCluster
	if psc.Status.PrimaryCluster != "" {
		primaryClusterName = psc.Status.PrimaryCluster
	}
	for _, cluster := range psc.Spec.Clusters {
		if cluster.Name == primaryClusterName {
			return &cluster
		}
	}
	return nil
}

func (psc *PerconaServerMySQLClusterSet) GetCluster(name string) *ClusterSetCluster {
	for _, cluster := range psc.Spec.Clusters {
		if cluster.Name == name {
			return &cluster
		}
	}
	return nil
}

func (pcs *PerconaServerMySQLClusterSet) UpdateStatus(ctx context.Context, cl client.Client, mutate func(status *PerconaServerMySQLClusterSetStatus)) error {
	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		actual := &PerconaServerMySQLClusterSet{}
		if err := cl.Get(ctx, types.NamespacedName{Name: pcs.Name, Namespace: pcs.Namespace}, actual); err != nil {
			return err
		}
		mutate(&actual.Status)
		return cl.Status().Update(ctx, actual)
	})
}
