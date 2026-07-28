package workloadidentity

import (
	"context"
	"errors"
	"fmt"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const RecoveryPreviousWorkloadIdentityUIDIndex = "workloadidentityrecovery.spec.previousWorkloadIdentityUid"

func IndexRecoveriesByPreviousWorkloadIdentityUID(
	ctx context.Context,
	indexer client.FieldIndexer,
) error {
	if err := indexer.IndexField(
		ctx,
		&workloadidentityv1alpha1.WorkloadIdentityRecovery{},
		RecoveryPreviousWorkloadIdentityUIDIndex,
		func(object client.Object) []string {
			recovery := object.(*workloadidentityv1alpha1.WorkloadIdentityRecovery)
			return []string{string(recovery.Spec.PreviousWorkloadIdentityUID)}
		},
	); err != nil {
		return fmt.Errorf("index WorkloadIdentityRecoveries by previous WorkloadIdentity UID: %w", err)
	}
	return nil
}

type RecoveryBlockedError struct {
	Reason  string
	Message string
}

func (e *RecoveryBlockedError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Reason
}

func NewRecoveryBlockedError(reason, message string) error {
	return &RecoveryBlockedError{Reason: reason, Message: message}
}

func RecoveryBlockedReason(err error) (string, bool) {
	blocked := &RecoveryBlockedError{}
	if !errors.As(err, &blocked) {
		return "", false
	}
	return blocked.Reason, true
}

type RecoveryDetector interface {
	DetectRecovery(
		ctx context.Context,
		identity *workloadidentityv1alpha1.WorkloadIdentity,
	) (RecoveryRequiredEvidence, error)
}

type RecoveryManager interface {
	Inspect(
		ctx context.Context,
		recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
		identity *workloadidentityv1alpha1.WorkloadIdentity,
		issuerURL, subject string,
	) (*workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan, error)
	MarkInProgress(
		ctx context.Context,
		recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
		identity *workloadidentityv1alpha1.WorkloadIdentity,
		plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
	) error
	EnsureFederatedIdentityCredential(
		ctx context.Context,
		recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
		identity *workloadidentityv1alpha1.WorkloadIdentity,
		plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
	) error
	Commit(
		ctx context.Context,
		recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
		identity *workloadidentityv1alpha1.WorkloadIdentity,
		plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
	) error
	Finalize(
		ctx context.Context,
		recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
		identity *workloadidentityv1alpha1.WorkloadIdentity,
		plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
	) error
}
