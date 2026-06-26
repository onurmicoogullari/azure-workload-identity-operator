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
)

type WorkloadIdentityConditionType string

const (
	WorkloadIdentityConditionReady WorkloadIdentityConditionType = "Ready"
)

// WorkloadIdentitySpec defines the desired state of WorkloadIdentity.
type WorkloadIdentitySpec struct {
	// azure configures the Azure managed identity and federated credential.
	// +required
	Azure AzureWorkloadIdentityConfig `json:"azure"`

	// serviceAccount is the Kubernetes ServiceAccount to create or adopt.
	// +required
	ServiceAccount ServiceAccountReference `json:"serviceAccount"`

	// deletionPolicy controls Azure and ServiceAccount deletion when this WorkloadIdentity is deleted.
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default=Retain
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

type AzureWorkloadIdentityConfig struct {
	// subscriptionID is the Azure subscription UUID containing identity resources.
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`
	// +required
	SubscriptionID string `json:"subscriptionID"`

	// location is the Azure region for identity resources.
	// +kubebuilder:validation:MinLength=1
	// +required
	Location string `json:"location"`

	// resourceGroupName is the Azure resource group containing the managed identity.
	// +kubebuilder:validation:MinLength=1
	// +required
	ResourceGroupName string `json:"resourceGroupName"`

	// userAssignedIdentityName is the Azure User Assigned Managed Identity name.
	// +kubebuilder:validation:MinLength=1
	// +required
	UserAssignedIdentityName string `json:"userAssignedIdentityName"`

	// federatedIdentityCredentialName is the Azure federated identity credential name.
	// +kubebuilder:validation:MinLength=1
	// +required
	FederatedIdentityCredentialName string `json:"federatedIdentityCredentialName"`
}

type ServiceAccountReference struct {
	// name is the ServiceAccount name.
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`
}

// WorkloadIdentityStatus defines the observed state of WorkloadIdentity.
type WorkloadIdentityStatus struct {
	// clientID is the Azure User Assigned Managed Identity client ID.
	// +optional
	ClientID string `json:"clientID,omitempty"`

	// principalID is the Azure User Assigned Managed Identity principal ID.
	// +optional
	PrincipalID string `json:"principalID,omitempty"`

	// tenantID is the Azure tenant ID for the managed identity.
	// +optional
	TenantID string `json:"tenantID,omitempty"`

	// issuerURL is the OIDC issuer URL used by the federated credential.
	// +optional
	IssuerURL string `json:"issuerURL,omitempty"`

	// subject is the Kubernetes service account subject trusted by Azure.
	// +optional
	Subject string `json:"subject,omitempty"`

	// observedGeneration is the latest generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// lastReconciledTime is when the controller last handled this resource.
	// +optional
	LastReconciledTime *metav1.Time `json:"lastReconciledTime,omitempty"`

	// azureResources contains the Azure resources owned or used by this workload identity.
	// +listType=map
	// +listMapKey=id
	// +optional
	AzureResources []AzureResource `json:"azureResources,omitempty"`

	// conditions represent the current state of the WorkloadIdentity resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Client ID",type=string,JSONPath=`.status.clientID`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// WorkloadIdentity is the Schema for the workloadidentities API.
type WorkloadIdentity struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of WorkloadIdentity
	// +required
	Spec WorkloadIdentitySpec `json:"spec"`

	// status defines the observed state of WorkloadIdentity
	// +optional
	Status WorkloadIdentityStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// WorkloadIdentityList contains a list of WorkloadIdentity
type WorkloadIdentityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []WorkloadIdentity `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &WorkloadIdentity{}, &WorkloadIdentityList{})
		return nil
	})
}
