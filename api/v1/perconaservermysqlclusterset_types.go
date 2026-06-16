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
// +kubebuilder:printcolumn:name="Primary",type=string,JSONPath=".status.primaryCluster"
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=".status.primaryClusterEndpoint"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
type PerconaServerMySQLClusterSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PerconaServerMySQLClusterSetSpec   `json:"spec,omitempty"`
	Status PerconaServerMySQLClusterSetStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.clusters.exists(c, c.innodbClusterName == self.primaryCluster)",message="spec.primaryCluster not found in spec.clusters"
// +kubebuilder:validation:XValidation:rule="self.clusters.all(c, c.endpoints.all(e, self.clusters.map(c2, c2.endpoints.filter(e2, e2.host == e.host).size()).sum() == 1))",message="endpoint host must be unique across all clusters"

type PerconaServerMySQLClusterSetSpec struct {
	// PrimaryCluster is the desired primary cluster of the ClusterSet.
	// This is the cluster that will serve writes, and replica members will connect to.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self.matches('^[A-Za-z0-9]+$')",message="primaryCluster must contain only alphanumeric characters"
	PrimaryCluster string `json:"primaryCluster"`

	// UnsafeClusterSetFlags is the configuration for the unsafe cluster set flags.
	// +kubebuilder:validation:Optional
	UnsafeClusterSetFlags *UnsafeClusterSetFlags `json:"unsafeFlags,omitempty"`

	// SSLMode is the desired SSL mode of the ClusterSet.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=AUTO;DISABLED;REQUIRED;VERIFY_CA;VERIFY_IDENTITY
	// +kubebuilder:default:=AUTO
	SSLMode *ClusterSetSSLMode `json:"sslMode,omitempty"`

	// CredentialsSecret is the secret containing the credentials for the ClusterSet.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="has(self.name) && self.name != ''",message="credentialsSecret.name must be set"
	CredentialsSecret corev1.SecretKeySelector `json:"credentialsSecret"`

	// Clusters is the list of member clusters in the ClusterSet.
	// At least one cluster must be specified (the primary).
	//
	// +listType=map
	// +listMapKey=innodbClusterName
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems:=1
	// +kubebuilder:validation:MaxItems:=10
	// +kubebuilder:validation:XValidation:rule="self.all(c, self.exists_one(d, d.innodbClusterName == c.innodbClusterName))",message="cluster names must be unique"
	Clusters []ClusterSetCluster `json:"clusters"`

	// CreateReplicaClusterOptions is the configuration for the creation of a replica cluster.
	// +kubebuilder:validation:Optional
	CreateReplicaClusterOptions CreateReplicaClusterOptions `json:"createReplicaClusterOptions"`

	// MysqlShellRunner is the configuration for the MySQL shell runner.
	// +kubebuilder:validation:Required
	MySQLShellRunner MySQLShellRunnerSpec `json:"mysqlshellRunner"`
}

type UnsafeClusterSetFlags struct {
	// ForcedFailover controls if an emergency failover may be performed if the
	// current primary is unreachable. Enabling it can promote a replica while
	// the old primary may still accept writes, which can cause split-brain,
	// lost transactions, and require manual recovery of the old primary.
	// Set this only when you're absolutely sure that your primary cannot be recovered.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=false
	ForcedFailover *bool `json:"forcedFailover,omitempty"`

	// ForcedClusterRemoval controls if a replica cluster can be removed if it is
	// unreachable. Enabling it lets the operator forget an unreachable replica;
	// any transactions not replicated back are abandoned.
	// This can effectively leave the replica cluster unusable, requiring manual cleanup or full rebuild.
	// Set this only if you can afford to discard the replica cluster.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=false
	ForcedClusterRemoval *bool `json:"forcedClusterRemoval,omitempty"`
}

func (ucs *UnsafeClusterSetFlags) SetDefaults() {
	if ucs.ForcedFailover == nil {
		ucs.ForcedFailover = new(false)
	}
	if ucs.ForcedClusterRemoval == nil {
		ucs.ForcedClusterRemoval = new(false)
	}
}

func (pcs *PerconaServerMySQLClusterSet) SetDefaults() {
	if pcs.Spec.CreateReplicaClusterOptions.RecoveryMethod == "" {
		pcs.Spec.CreateReplicaClusterOptions.RecoveryMethod = RecoveryMethodClone
	}

	if pcs.Spec.UnsafeClusterSetFlags == nil {
		pcs.Spec.UnsafeClusterSetFlags = &UnsafeClusterSetFlags{}
	}
	pcs.Spec.UnsafeClusterSetFlags.SetDefaults()

	if pcs.Spec.SSLMode == nil {
		pcs.Spec.SSLMode = new(ClusterSetSSLModeAuto)
	}

	for i := range pcs.Spec.Clusters {
		for j := range pcs.Spec.Clusters[i].Endpoints {
			if pcs.Spec.Clusters[i].Endpoints[j].Port == nil {
				pcs.Spec.Clusters[i].Endpoints[j].Port = new(int32(3306))
			}
		}
	}
}

type RecoveryMethod string

const (
	RecoveryMethodClone       RecoveryMethod = "clone"
	RecoveryMethodIncremental RecoveryMethod = "incremental"
	RecoveryMethodAuto        RecoveryMethod = "auto"
)

type CreateReplicaClusterOptions struct {
	// Preferred method for state recovery/provisioning.
	// Default is 'clone'.
	// Set this to 'incremental' when the cluster is seeded from an existing backup.
	//
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=clone;incremental;auto
	// +kubebuilder:default:=clone
	RecoveryMethod RecoveryMethod `json:"recoveryMethod"`
}

type ClusterSetSSLMode string

const (
	// TLS encryption will be enabled if supported by the instance, otherwise disabled.
	ClusterSetSSLModeAuto ClusterSetSSLMode = "AUTO"
	// TLS encryption will be disabled.
	ClusterSetSSLModeDisabled ClusterSetSSLMode = "DISABLED"
	// TLS encryption will be enabled.
	ClusterSetSSLModeRequired ClusterSetSSLMode = "REQUIRED"
	// Like REQUIRED, but additionally verify the peer server TLS certificate against the configured Certificate Authority (CA) certificates.
	// In this case, the replica and primary certificates must be signed by the same Certificate Authority (CA).
	ClusterSetSSLModeVerifyCA ClusterSetSSLMode = "VERIFY_CA"
	// Like VERIFY_CA, but additionally verify that the peer server certificate matches the host to which the connection is attempted.
	// The primary cluster's server cert must have a SAN matching the ClusterSetCluster.Endpoints[].Host value the replica connects to
	ClusterSetSSLModeVerifyIdentity ClusterSetSSLMode = "VERIFY_IDENTITY"
)

type MySQLShellRunnerSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`
}

type ClusterSetCluster struct {
	// InnoDBClusterName is the name of the InnoDB cluster that will be a member of this ClusterSet.
	// This name must be unique within the ClusterSet.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self.matches('^[A-Za-z0-9]+$')",message="innodbClusterName must contain only alphanumeric characters"
	InnoDBClusterName string `json:"innodbClusterName"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems:=1
	// +kubebuilder:validation:MaxItems:=9
	Endpoints []ClusterSetClusterEndpoint `json:"endpoints"`
}

type ClusterSetClusterEndpoint struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="isIP(self) || !format.dns1123Subdomain().validate(self).hasValue()",message="host must be a valid IP address or domain name"
	Host string `json:"host"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=3306
	Port *int32 `json:"port,omitempty"`
}

func (ep *ClusterSetClusterEndpoint) GetPort() int32 {
	if ep.Port != nil {
		return *ep.Port
	}
	return 3306
}

const (
	EventTypeClusterSetPrimarySwitched       string = "ClusterSetPrimarySwitched"
	EventTypeClusterSetPrimaryForcedSwitched string = "ClusterSetPrimaryForcedSwitched"
	EventTypeClusterSetBootstrapped          string = "ClusterSetBootstrapped"
	EventTypeClusterSetUnhealthy             string = "ClusterSetHealthDegraded"
	EventTypeClusterSetMemberAdded           string = "ClusterSetMemberAdded"
	EventTypeClusterSetMemberRemoved         string = "ClusterSetMemberRemoved"
)

const (
	ConditionMySQLShellRunnerReady             string = "MySQLShellRunnerReady"
	ConditionClusterSetBootstrapped            string = "ClusterSetBootstrapped"
	ConditionClusterSetPrimarySwitchOverInProg string = "SwitchoverInProgress"
	ConditionReplicaManagementFailure          string = "ReplicaManagementFailure"
	ConditionPrimaryClusterUnreachable         string = "PrimaryClusterUnreachable"
	ConditionClusterSetDissolving              string = "ClusterSetDissolving"
	ConditionClusterSetReady                   string = "Ready"
)

type ClusterSetStatus map[string]ClusterSetClusterStatus

type PerconaServerMySQLClusterSetStatus struct {
	PrimaryCluster         string             `json:"primaryCluster"`
	PrimaryClusterEndpoint string             `json:"primaryClusterEndpoint"`
	Conditions             []metav1.Condition `json:"conditions,omitempty"`
	Clusters               ClusterSetStatus   `json:"clusters,omitempty"`
	ObservedGeneration     int64              `json:"lastObservedGeneration,omitempty"`
	LastObservedAt         metav1.Time        `json:"lastObservedAt,omitempty"`
}

// ClusterSetClusterStatus is the status of a single cluster in the cluster set.
// The shape of this object is derived from the output of `dba.getCluster().getClusterSet().status()` mysqlshell command.
type ClusterSetClusterStatus struct {
	ClusterRole  string `json:"clusterRole"`
	GlobalStatus string `json:"globalStatus"`
	Primary      string `json:"primary"`
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
	for i := range psc.Spec.Clusters {
		if psc.Spec.Clusters[i].InnoDBClusterName == primaryClusterName {
			return &psc.Spec.Clusters[i]
		}
	}
	return nil
}

func (psc *PerconaServerMySQLClusterSet) GetCluster(name string) *ClusterSetCluster {
	for i := range psc.Spec.Clusters {
		if psc.Spec.Clusters[i].InnoDBClusterName == name {
			return &psc.Spec.Clusters[i]
		}
	}
	return nil
}

func (pcs *PerconaServerMySQLClusterSet) UpdateStatus(ctx context.Context, cl client.Client, mutate func(status *PerconaServerMySQLClusterSetStatus) error) error {
	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		actual := &PerconaServerMySQLClusterSet{}
		if err := cl.Get(ctx, types.NamespacedName{Name: pcs.Name, Namespace: pcs.Namespace}, actual); err != nil {
			return err
		}
		if err := mutate(&actual.Status); err != nil {
			return err
		}
		actual.Status.ObservedGeneration = actual.Generation
		actual.Status.LastObservedAt = metav1.Now()

		return cl.Status().Update(ctx, actual)
	})
}
