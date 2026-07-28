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

package controller

import (
	"context"
	"fmt"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

func (r *WorkloadIdentityRecoveryReconciler) getCurrentTarget(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
) (*workloadidentityv1alpha1.WorkloadIdentity, error) {
	identity := &workloadidentityv1alpha1.WorkloadIdentity{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: recovery.Spec.WorkloadIdentityRef.Namespace,
		Name:      recovery.Spec.WorkloadIdentityRef.Name,
	}, identity)
	return identity, err
}

func validateRecoveryControllerTarget(
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
) error {
	if identity.UID != recovery.Spec.WorkloadIdentityRef.UID {
		return fmt.Errorf(
			"target WorkloadIdentity UID is %q, expected %q",
			identity.UID,
			recovery.Spec.WorkloadIdentityRef.UID,
		)
	}
	if recovery.Spec.PreviousWorkloadIdentityUID == identity.UID {
		return fmt.Errorf("previous WorkloadIdentity UID must differ from the current UID")
	}
	if identity.Status.Recovery == nil ||
		identity.Status.Recovery.PreviousWorkloadIdentityUID != recovery.Spec.PreviousWorkloadIdentityUID {
		return fmt.Errorf("target WorkloadIdentity recovery evidence changed")
	}
	ready := apimeta.FindStatusCondition(
		identity.Status.Conditions,
		string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
	)
	if ready == nil || ready.Status != metav1.ConditionFalse {
		return fmt.Errorf("target WorkloadIdentity is not in an active recovery state")
	}
	if recovery.Status.MutationStarted {
		if ready.Reason != workloadidentity.ReasonRecoveryInProgress {
			return fmt.Errorf("target WorkloadIdentity is not in RecoveryInProgress state")
		}
		return nil
	}
	if ready.Reason == workloadidentity.ReasonRecoveryInProgress {
		// The process may have stopped after acquiring the target lock but
		// before persisting MutationStarted. The authoritative winner resumes
		// that forward transition in startRecovery.
		return nil
	}
	if !identity.DeletionTimestamp.IsZero() {
		return fmt.Errorf("target WorkloadIdentity is being deleted")
	}
	if ready.ObservedGeneration != identity.Generation ||
		ready.Reason != workloadidentity.ReasonRecoveryRequired {
		return fmt.Errorf("target WorkloadIdentity is not in current-generation recovery state")
	}
	return nil
}

func (r *WorkloadIdentityRecoveryReconciler) recoveryWinnerForSource(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
) (*workloadidentityv1alpha1.WorkloadIdentityRecovery, error) {
	recoveries := &workloadidentityv1alpha1.WorkloadIdentityRecoveryList{}
	if err := r.recoveryReader().List(ctx, recoveries); err != nil {
		return nil, fmt.Errorf("list WorkloadIdentityRecoveries for previous UID: %w", err)
	}
	var incumbent *workloadidentityv1alpha1.WorkloadIdentityRecovery
	var winner *workloadidentityv1alpha1.WorkloadIdentityRecovery
	for i := range recoveries.Items {
		candidate := &recoveries.Items[i]
		if candidate.Spec.PreviousWorkloadIdentityUID != recovery.Spec.PreviousWorkloadIdentityUID {
			continue
		}
		if candidate.Status.MutationStarted || candidate.Status.CommitVerified {
			if incumbent == nil || recoveryPrecedes(candidate, incumbent) {
				incumbent = candidate
			}
		}
		if winner == nil || recoveryPrecedes(candidate, winner) {
			winner = candidate
		}
	}
	if incumbent != nil {
		winner = incumbent
	}
	if winner == nil || winner.UID == recovery.UID {
		return nil, nil
	}
	return winner, nil
}

func recoveryPrecedes(
	left, right *workloadidentityv1alpha1.WorkloadIdentityRecovery,
) bool {
	switch {
	case left.CreationTimestamp.Before(&right.CreationTimestamp):
		return true
	case right.CreationTimestamp.Before(&left.CreationTimestamp):
		return false
	case left.UID != right.UID:
		return string(left.UID) < string(right.UID)
	default:
		return left.Name < right.Name
	}
}

func (r *WorkloadIdentityRecoveryReconciler) markTargetRecoveryInProgress(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
) error {
	return r.patchTargetRecoveryCondition(
		ctx,
		recovery,
		identity,
		[]string{workloadidentity.ReasonRecoveryRequired, workloadidentity.ReasonRecoveryInProgress},
		workloadidentity.ReasonRecoveryInProgress,
		"Controlled recovery is in progress",
	)
}

func (r *WorkloadIdentityRecoveryReconciler) releaseCompletedTarget(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
) error {
	return r.patchTargetRecoveryCondition(
		ctx,
		recovery,
		recoveryTargetFromReference(recovery),
		[]string{workloadidentity.ReasonRecoveryInProgress},
		recoveryReasonCompleted,
		"Controlled recovery completed; normal reconciliation will resume",
	)
}

func (r *WorkloadIdentityRecoveryReconciler) releaseCancelledTarget(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
) error {
	return r.patchTargetRecoveryCondition(
		ctx,
		recovery,
		recoveryTargetFromReference(recovery),
		[]string{workloadidentity.ReasonRecoveryInProgress},
		recoveryReasonCancelled,
		"Controlled recovery was cancelled before mutation",
	)
}

func (r *WorkloadIdentityRecoveryReconciler) patchTargetRecoveryCondition(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	fromReasons []string,
	reason, message string,
) error {
	current := &workloadidentityv1alpha1.WorkloadIdentity{}
	if err := r.recoveryReader().Get(ctx, client.ObjectKeyFromObject(identity), current); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if current.UID != recovery.Spec.WorkloadIdentityRef.UID ||
		current.Status.Recovery == nil ||
		current.Status.Recovery.PreviousWorkloadIdentityUID != recovery.Spec.PreviousWorkloadIdentityUID {
		return nil
	}
	ready := apimeta.FindStatusCondition(
		current.Status.Conditions,
		string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
	)
	if ready == nil || ready.Status != metav1.ConditionFalse ||
		!slices.Contains(fromReasons, ready.Reason) {
		return nil
	}
	original := current.DeepCopy()
	apimeta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{
		Type:               string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: current.Generation,
	})
	return r.Status().Patch(ctx, current, client.MergeFrom(original))
}

func recoveryTargetFromReference(
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
) *workloadidentityv1alpha1.WorkloadIdentity {
	return &workloadidentityv1alpha1.WorkloadIdentity{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: recovery.Spec.WorkloadIdentityRef.Namespace,
			Name:      recovery.Spec.WorkloadIdentityRef.Name,
			UID:       recovery.Spec.WorkloadIdentityRef.UID,
		},
	}
}

func (r *WorkloadIdentityRecoveryReconciler) recoveryReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *WorkloadIdentityRecoveryReconciler) recoveriesForWorkloadIdentity(
	ctx context.Context,
	object client.Object,
) []reconcile.Request {
	recoveries := &workloadidentityv1alpha1.WorkloadIdentityRecoveryList{}
	targetKey := types.NamespacedName{
		Namespace: object.GetNamespace(),
		Name:      object.GetName(),
	}
	if err := r.List(ctx, recoveries, client.MatchingFields{
		workloadIdentityRecoveryTargetIndex: targetKey.String(),
	}); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list WorkloadIdentityRecoveries for WorkloadIdentity watch")
		return nil
	}
	requests := make([]reconcile.Request, 0, 1)
	for i := range recoveries.Items {
		recovery := &recoveries.Items[i]
		ref := recovery.Spec.WorkloadIdentityRef
		if recovery.DeletionTimestamp.IsZero() &&
			!recoveryIsTerminal(recovery) &&
			ref.Namespace == targetKey.Namespace &&
			ref.Name == targetKey.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: recovery.Name},
			})
		}
	}
	return requests
}

func activeRecoveryTargetIndexValues(object client.Object) []string {
	recovery := object.(*workloadidentityv1alpha1.WorkloadIdentityRecovery)
	if !recovery.DeletionTimestamp.IsZero() || recoveryIsTerminal(recovery) {
		return nil
	}
	ref := recovery.Spec.WorkloadIdentityRef
	return []string{types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}.String()}
}
