/*
Copyright 2026.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

type WorkloadIdentityRecoveryConditionType string

const (
	WorkloadIdentityRecoveryConditionComplete    WorkloadIdentityRecoveryConditionType = "Complete"
	WorkloadIdentityRecoveryConditionProgressing WorkloadIdentityRecoveryConditionType = "Progressing"
	WorkloadIdentityRecoveryConditionBlocked     WorkloadIdentityRecoveryConditionType = "Blocked"
	WorkloadIdentityRecoveryConditionFailed      WorkloadIdentityRecoveryConditionType = "Failed"
)

// WorkloadIdentityRecoveryReference identifies the current WorkloadIdentity instance.
// +kubebuilder:validation:XValidation:rule="size(self.uid) > 0",message="uid must not be empty"
type WorkloadIdentityRecoveryReference struct {
	// namespace is the namespace of the current WorkloadIdentity.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +required
	Namespace string `json:"namespace"`

	// name is the name of the current WorkloadIdentity.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +required
	Name string `json:"name"`

	// uid is the UID of the current WorkloadIdentity instance.
	// +required
	UID types.UID `json:"uid"`
}

// WorkloadIdentityRecoverySpec defines the desired state of WorkloadIdentityRecovery.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
// +kubebuilder:validation:XValidation:rule="size(self.previousWorkloadIdentityUid) > 0",message="previousWorkloadIdentityUid must not be empty"
// +kubebuilder:validation:XValidation:rule="self.previousWorkloadIdentityUid != self.workloadIdentityRef.uid",message="previousWorkloadIdentityUid must differ from the current WorkloadIdentity uid"
type WorkloadIdentityRecoverySpec struct {
	// workloadIdentityRef identifies the current WorkloadIdentity instance that will receive ownership.
	// +required
	WorkloadIdentityRef WorkloadIdentityRecoveryReference `json:"workloadIdentityRef"`

	// previousWorkloadIdentityUid is the UID currently recorded on the retained Azure identity.
	// +required
	PreviousWorkloadIdentityUID types.UID `json:"previousWorkloadIdentityUid"`
}

// WorkloadIdentityRecoveryPlan records the verified Azure identity and exact
// trust tuple used by every forward recovery retry.
type WorkloadIdentityRecoveryPlan struct {
	// userAssignedIdentity records the verified retained Azure identity.
	// +required
	UserAssignedIdentity WorkloadIdentityRecoveryUserAssignedIdentity `json:"userAssignedIdentity"`

	// federatedIdentityCredential records the exact trust tuple recovery applies.
	// +required
	FederatedIdentityCredential WorkloadIdentityRecoveryFederatedIdentityCredential `json:"federatedIdentityCredential"`
}

// WorkloadIdentityRecoveryUserAssignedIdentity records immutable Azure identity properties.
type WorkloadIdentityRecoveryUserAssignedIdentity struct {
	// id is the Azure resource ID.
	// +required
	ID string `json:"id"`

	// clientId is the managed identity client ID.
	// +required
	ClientID string `json:"clientId"`

	// principalId is the managed identity principal ID.
	// +required
	PrincipalID string `json:"principalId"`

	// tenantId is the managed identity tenant ID.
	// +required
	TenantID string `json:"tenantId"`
}

// WorkloadIdentityRecoveryFederatedIdentityCredential records the exact trust
// tuple that recovery is authorized to apply.
type WorkloadIdentityRecoveryFederatedIdentityCredential struct {
	// issuer is the trusted token issuer.
	// +required
	Issuer string `json:"issuer"`

	// subject is the trusted Kubernetes ServiceAccount subject.
	// +required
	Subject string `json:"subject"`

	// audiences are the trusted token audiences.
	// +listType=set
	// +required
	Audiences []string `json:"audiences"`
}

// WorkloadIdentityRecoveryStatus defines the observed state of WorkloadIdentityRecovery.
type WorkloadIdentityRecoveryStatus struct {
	// observedGeneration is the latest generation handled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// lastAttemptTime is when reconciliation last attempted recovery.
	// +optional
	LastAttemptTime *metav1.Time `json:"lastAttemptTime,omitempty"`

	// startedTime is when the first external recovery mutation began.
	// +optional
	StartedTime *metav1.Time `json:"startedTime,omitempty"`

	// completedTime is when recovery completion was read-verified.
	// +optional
	CompletedTime *metav1.Time `json:"completedTime,omitempty"`

	// mutationStarted indicates that recovery is forward-only and must reach a
	// completed state before its finalizer can be removed.
	// +optional
	MutationStarted bool `json:"mutationStarted,omitempty"`

	// commitVerified indicates that UAMI ownership transfer was read-verified.
	// +optional
	CommitVerified bool `json:"commitVerified,omitempty"`

	// plan is the read-verified identity and trust tuple captured before mutation.
	// +optional
	Plan *WorkloadIdentityRecoveryPlan `json:"plan,omitempty"`

	// conditions represent the current state of the WorkloadIdentityRecovery resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Complete",type=string,JSONPath=`.status.conditions[?(@.type=='Complete')].status`
// +kubebuilder:printcolumn:name="Blocked",type=string,JSONPath=`.status.conditions[?(@.type=='Blocked')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// WorkloadIdentityRecovery is the Schema for the workloadidentityrecoveries API.
type WorkloadIdentityRecovery struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of WorkloadIdentityRecovery
	// +required
	Spec WorkloadIdentityRecoverySpec `json:"spec"`

	// status defines the observed state of WorkloadIdentityRecovery
	// +optional
	Status WorkloadIdentityRecoveryStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// WorkloadIdentityRecoveryList contains a list of WorkloadIdentityRecovery
type WorkloadIdentityRecoveryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []WorkloadIdentityRecovery `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &WorkloadIdentityRecovery{}, &WorkloadIdentityRecoveryList{})
		return nil
	})
}
