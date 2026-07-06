package workloadidentity

import (
	"context"
	"errors"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
)

const (
	ReasonAzureResourceOwnershipConflict      = "AzureResourceOwnershipConflict"
	ReasonFederatedIdentityCredentialConflict = "FederatedIdentityCredentialConflict"
)

type ManagedIdentity struct {
	ClientID       string
	PrincipalID    string
	TenantID       string
	AzureResources []azworkloadidentityv1alpha1.AzureResource
}

type DeleteOptions struct {
	PreserveResourceGroup            bool
	PreserveUserAssignedIdentity     bool
	ResourceGroupSuccessorUID        string
	UserAssignedIdentitySuccessorUID string
}

type ConflictError struct {
	Reason  string
	Message string
}

func (e *ConflictError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Reason
}

func NewConflictError(reason, message string) error {
	return &ConflictError{Reason: reason, Message: message}
}

func ConflictReason(err error) (string, bool) {
	conflict := &ConflictError{}
	if !errors.As(err, &conflict) {
		return "", false
	}
	return conflict.Reason, true
}

type Manager interface {
	Ensure(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, issuerURL, subject string) (ManagedIdentity, error)
	Delete(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) error
}

type DependencyAwareManager interface {
	Manager
	DeleteWithOptions(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, options DeleteOptions) error
}
