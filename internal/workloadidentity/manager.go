package workloadidentity

import (
	"context"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
)

type ManagedIdentity struct {
	ClientID       string
	PrincipalID    string
	TenantID       string
	AzureResources []azworkloadidentityv1alpha1.AzureResource
}

type Manager interface {
	Ensure(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, issuerURL, subject string) (ManagedIdentity, error)
	Delete(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) error
}
