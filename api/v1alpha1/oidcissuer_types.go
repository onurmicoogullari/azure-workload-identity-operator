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

type DeletionPolicy string

const (
	DeletionPolicyRetain DeletionPolicy = "Retain"
	DeletionPolicyDelete DeletionPolicy = "Delete"
)

type OIDCIssuerConditionType string

const (
	OIDCIssuerConditionReady OIDCIssuerConditionType = "Ready"
)

type SigningKeyState string

const (
	SigningKeyStateActive   SigningKeyState = "Active"
	SigningKeyStateRetiring SigningKeyState = "Retiring"
)

const OIDCIssuerName = "default"

// OIDCIssuerSpec defines the desired state of OIDCIssuer.
type OIDCIssuerSpec struct {
	// azure configures Azure resources used to publish OIDC issuer documents.
	// +required
	Azure AzureOIDCIssuerConfig `json:"azure"`

	// signingKey points at the Kubernetes service-account signing key used to publish JWKS.
	// +required
	SigningKey SigningKeySource `json:"signingKey"`

	// openShift configures optional OpenShift-specific integration.
	// +optional
	OpenShift *OpenShiftOIDCIssuerConfig `json:"openShift,omitempty"`

	// deletionPolicy controls Azure resource deletion when this OIDCIssuer is deleted.
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default=Retain
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

type AzureOIDCIssuerConfig struct {
	// subscriptionID is the Azure subscription UUID containing issuer storage resources.
	// +kubebuilder:validation:Pattern=`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`
	// +required
	SubscriptionID string `json:"subscriptionID"`

	// location is the Azure region for issuer storage resources.
	// +kubebuilder:validation:MinLength=1
	// +required
	Location string `json:"location"`

	// resourceGroupName is the Azure resource group containing issuer storage.
	// +kubebuilder:validation:MinLength=1
	// +required
	ResourceGroupName string `json:"resourceGroupName"`

	// storageAccountName is the Azure Storage account that serves OIDC discovery documents.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]{3,24}$`
	// +required
	StorageAccountName string `json:"storageAccountName"`

	// blobContainerName is the public blob container for discovery and JWKS documents.
	// +kubebuilder:default=oidc
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=63
	// +required
	BlobContainerName string `json:"blobContainerName"`
}

type SecretKeyReference struct {
	// name is the Secret name.
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`

	// namespace is the Secret namespace.
	// +kubebuilder:validation:MinLength=1
	// +required
	Namespace string `json:"namespace"`

	// key is the Secret data key.
	// +kubebuilder:validation:MinLength=1
	// +required
	Key string `json:"key"`
}

type SigningKeySource struct {
	// secretRef references the active Secret containing a PEM public or private signing key.
	// +required
	SecretRef SecretKeyReference `json:"secretRef"`

	// retiringSecretRef references the previous signing key that should remain published in JWKS
	// while service account tokens signed by it can still be valid.
	// +optional
	RetiringSecretRef *SecretKeyReference `json:"retiringSecretRef,omitempty"`
}

type OpenShiftOIDCIssuerConfig struct {
	// updateServiceAccountIssuer updates OpenShift Authentication.spec.serviceAccountIssuer.
	// +optional
	UpdateServiceAccountIssuer bool `json:"updateServiceAccountIssuer,omitempty"`
}

// OIDCIssuerStatus defines the observed state of OIDCIssuer.
type OIDCIssuerStatus struct {
	// issuerURL is the public OIDC issuer URL used in Azure federated identity credentials.
	// +optional
	IssuerURL string `json:"issuerURL,omitempty"`

	// observedGeneration is the latest generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// lastReconciledTime is when the controller last handled this resource.
	// +optional
	LastReconciledTime *metav1.Time `json:"lastReconciledTime,omitempty"`

	// azureResources contains the Azure resources owned or used by this issuer.
	// +listType=map
	// +listMapKey=id
	// +optional
	AzureResources []AzureResource `json:"azureResources,omitempty"`

	// signingKeys contains the public signing keys currently published in JWKS.
	// +listType=map
	// +listMapKey=kid
	// +optional
	SigningKeys []SigningKeyStatus `json:"signingKeys,omitempty"`

	// previousServiceAccountIssuer is the OpenShift Authentication.spec.serviceAccountIssuer value
	// captured before this OIDCIssuer updated it. A present empty string means the previous value was empty.
	// +optional
	PreviousServiceAccountIssuer *string `json:"previousServiceAccountIssuer,omitempty"`

	// conditions represent the current state of the OIDCIssuer resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type AzureResource struct {
	// id is the Azure resource ID.
	// +kubebuilder:validation:MinLength=1
	// +required
	ID string `json:"id"`

	// kind is the Azure resource kind, for example ResourceGroup, StorageAccount, or BlobContainer.
	// +kubebuilder:validation:MinLength=1
	// +required
	Kind string `json:"kind"`
}

type SigningKeyStatus struct {
	// kid is the JSON Web Key ID published for this signing key.
	// +kubebuilder:validation:MinLength=1
	// +required
	KID string `json:"kid"`

	// algorithm is the JOSE signing algorithm advertised for this key.
	// +kubebuilder:validation:MinLength=1
	// +required
	Algorithm string `json:"algorithm"`

	// state indicates whether this key is the active signing key or a retiring key.
	// +kubebuilder:validation:Enum=Active;Retiring
	// +required
	State SigningKeyState `json:"state"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Issuer URL",type=string,JSONPath=`.status.issuerURL`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OIDCIssuer is the Schema for the oidcissuers API.
type OIDCIssuer struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of OIDCIssuer
	// +required
	Spec OIDCIssuerSpec `json:"spec"`

	// status defines the observed state of OIDCIssuer
	// +optional
	Status OIDCIssuerStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// OIDCIssuerList contains a list of OIDCIssuer.
type OIDCIssuerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []OIDCIssuer `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &OIDCIssuer{}, &OIDCIssuerList{})
		return nil
	})
}
