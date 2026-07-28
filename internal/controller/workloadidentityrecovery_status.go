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

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

func (r *WorkloadIdentityRecoveryReconciler) handleRecoveryError(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	err error,
) (ctrl.Result, error) {
	if reason, ok := workloadidentity.RecoveryBlockedReason(err); ok {
		return r.block(ctx, recovery, reason, err.Error())
	}
	return ctrl.Result{}, err
}

func (r *WorkloadIdentityRecoveryReconciler) block(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	reason, message string,
) (ctrl.Result, error) {
	if err := r.patchRecoveryStatus(ctx, recovery, func(
		status *workloadidentityv1alpha1.WorkloadIdentityRecoveryStatus,
	) {
		setRecoveryCondition(
			status,
			workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionBlocked,
			metav1.ConditionTrue,
			reason,
			message,
			recovery.Generation,
		)
		setRecoveryCondition(
			status,
			workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionProgressing,
			metav1.ConditionFalse,
			reason,
			message,
			recovery.Generation,
		)
		clearRecoveryCondition(status, workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionComplete)
		clearRecoveryCondition(status, workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionFailed)
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: recoveryRetryInterval}, nil
}

func (r *WorkloadIdentityRecoveryReconciler) fail(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	reason, message string,
) (ctrl.Result, error) {
	if err := r.patchRecoveryStatus(ctx, recovery, func(
		status *workloadidentityv1alpha1.WorkloadIdentityRecoveryStatus,
	) {
		setRecoveryCondition(
			status,
			workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionFailed,
			metav1.ConditionTrue,
			reason,
			message,
			recovery.Generation,
		)
		setRecoveryCondition(
			status,
			workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionProgressing,
			metav1.ConditionFalse,
			recoveryReasonFailed,
			message,
			recovery.Generation,
		)
		clearRecoveryCondition(status, workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionBlocked)
		clearRecoveryCondition(status, workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionComplete)
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *WorkloadIdentityRecoveryReconciler) patchRecoveryStatus(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	mutate func(*workloadidentityv1alpha1.WorkloadIdentityRecoveryStatus),
) error {
	current := &workloadidentityv1alpha1.WorkloadIdentityRecovery{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(recovery), current); err != nil {
		return err
	}
	original := current.DeepCopy()
	now := metav1.Now()
	current.Status.ObservedGeneration = current.Generation
	current.Status.LastAttemptTime = &now
	mutate(&current.Status)
	if err := r.Status().Patch(ctx, current, client.MergeFrom(original)); err != nil {
		return err
	}
	recovery.Status = current.Status
	return nil
}

func setRecoveryCondition(
	status *workloadidentityv1alpha1.WorkloadIdentityRecoveryStatus,
	conditionType workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionType,
	conditionStatus metav1.ConditionStatus,
	reason, message string,
	generation int64,
) {
	apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               string(conditionType),
		Status:             conditionStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}

func clearRecoveryCondition(
	status *workloadidentityv1alpha1.WorkloadIdentityRecoveryStatus,
	conditionType workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionType,
) {
	apimeta.RemoveStatusCondition(&status.Conditions, string(conditionType))
}
