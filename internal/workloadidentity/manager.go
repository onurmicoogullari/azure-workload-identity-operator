package workloadidentity

import (
	"context"
	"errors"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	ReasonAzureResourceOwnershipConflict      = "AzureResourceOwnershipConflict"
	ReasonFederatedIdentityCredentialConflict = "FederatedIdentityCredentialConflict"
	ReasonRecoveryInProgress                  = "RecoveryInProgress"
	ReasonRecoveryRequired                    = "RecoveryRequired"
)

type ManagedIdentity struct {
	ClientID       string
	PrincipalID    string
	TenantID       string
	AzureResources []azworkloadidentityv1alpha1.AzureResource
}

type ConflictError struct {
	Reason           string
	Message          string
	RecoveryRequired *RecoveryRequiredEvidence
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

type RecoveryRequiredEvidence struct {
	PreviousWorkloadIdentityUID types.UID
}

func NewRecoveryRequiredError(message string, previousWorkloadIdentityUID types.UID) error {
	return &ConflictError{
		Reason:  ReasonRecoveryRequired,
		Message: message,
		RecoveryRequired: &RecoveryRequiredEvidence{
			PreviousWorkloadIdentityUID: previousWorkloadIdentityUID,
		},
	}
}

func ConflictReason(err error) (string, bool) {
	conflict := &ConflictError{}
	if !errors.As(err, &conflict) {
		return "", false
	}
	return conflict.Reason, true
}

func RecoveryRequiredDetails(err error) (RecoveryRequiredEvidence, bool) {
	conflict := &ConflictError{}
	if !errors.As(err, &conflict) || conflict.RecoveryRequired == nil {
		return RecoveryRequiredEvidence{}, false
	}
	return *conflict.RecoveryRequired, true
}

type Manager interface {
	Ensure(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, issuerURL, subject string) (ManagedIdentity, error)
	Delete(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) error
}
