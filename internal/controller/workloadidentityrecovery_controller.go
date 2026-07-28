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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerpkg "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

const (
	workloadIdentityRecoveryFinalizer   = "workloadidentity.azure.micosolutions.se/recovery-finalizer"
	workloadIdentityRecoveryTargetIndex = "workloadidentityrecovery.activeWorkloadIdentityRef"
	recoveryRetryInterval               = 30 * time.Second

	recoveryReasonCompleted            = "RecoveryCompleted"
	recoveryReasonCancelled            = "RecoveryCancelled"
	recoveryReasonCommitVerified       = "CommitVerified"
	recoveryReasonDuplicate            = "DuplicateRecovery"
	recoveryReasonFailed               = "RecoveryFailed"
	recoveryReasonManagerNotConfigured = "ManagerNotConfigured"
	recoveryReasonOIDCIssuerNotReady   = "OIDCIssuerNotReady"
	recoveryReasonPlanMissing          = "RecoveryPlanMissing"
	recoveryReasonServiceAccount       = "ServiceAccountConflict"
	recoveryReasonTargetChanged        = "TargetChanged"
	recoveryReasonTargetNotFound       = "TargetNotFound"
)

// WorkloadIdentityRecoveryReconciler reconciles a WorkloadIdentityRecovery object.
type WorkloadIdentityRecoveryReconciler struct {
	client.Client
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Manager   workloadidentity.RecoveryManager
}

// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=workloadidentityrecoveries,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=workloadidentityrecoveries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=workloadidentityrecoveries/finalizers,verbs=update
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=workloadidentities,verbs=get;list;watch
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=workloadidentities/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=oidcissuers,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;update;patch

func (r *WorkloadIdentityRecoveryReconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {
	recovery := &workloadidentityv1alpha1.WorkloadIdentityRecovery{}
	if err := r.Get(ctx, req.NamespacedName, recovery); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !recovery.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, recovery)
	}
	if !controllerutil.ContainsFinalizer(recovery, workloadIdentityRecoveryFinalizer) {
		controllerutil.AddFinalizer(recovery, workloadIdentityRecoveryFinalizer)
		if err := r.Update(ctx, recovery); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if recoveryIsComplete(recovery) {
		return ctrl.Result{}, r.releaseCompletedTarget(ctx, recovery)
	}
	if recoveryIsFailed(recovery) {
		return ctrl.Result{}, nil
	}
	if r.Manager == nil {
		return r.block(
			ctx,
			recovery,
			recoveryReasonManagerNotConfigured,
			"Azure recovery manager is not configured",
		)
	}
	return r.reconcileForward(ctx, recovery)
}

func recoveryIsTerminal(recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery) bool {
	return recoveryIsComplete(recovery) || recoveryIsFailed(recovery)
}

func recoveryIsComplete(recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery) bool {
	return apimeta.IsStatusConditionTrue(
		recovery.Status.Conditions,
		string(workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionComplete),
	)
}

func recoveryIsFailed(recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery) bool {
	return apimeta.IsStatusConditionTrue(
		recovery.Status.Conditions,
		string(workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionFailed),
	)
}

func (r *WorkloadIdentityRecoveryReconciler) reconcileForward(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
) (ctrl.Result, error) {
	identity, err := r.getCurrentTarget(ctx, recovery)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if recovery.Status.MutationStarted {
				return r.block(
					ctx,
					recovery,
					recoveryReasonTargetNotFound,
					"Target WorkloadIdentity must be restored so forward recovery can finish",
				)
			}
			return r.fail(ctx, recovery, recoveryReasonTargetNotFound, err.Error())
		}
		return ctrl.Result{}, err
	}
	if err := validateRecoveryControllerTarget(recovery, identity); err != nil {
		if recovery.Status.MutationStarted {
			return r.block(ctx, recovery, recoveryReasonTargetChanged, err.Error())
		}
		return r.fail(ctx, recovery, recoveryReasonTargetChanged, err.Error())
	}

	if recovery.Status.CommitVerified {
		if recovery.Status.Plan == nil {
			return r.block(ctx, recovery, recoveryReasonPlanMissing, "Recovery plan is missing after commit")
		}
		return r.finalizeRecovery(ctx, recovery, identity)
	}

	if recovery.Status.Plan == nil {
		if recovery.Status.MutationStarted {
			return r.block(ctx, recovery, recoveryReasonPlanMissing, "Recovery plan is missing after mutation started")
		}
		return r.preflightRecovery(ctx, recovery, identity)
	}

	if !recovery.Status.MutationStarted {
		return r.startRecovery(ctx, recovery, identity)
	}
	return r.advanceRecovery(ctx, recovery, identity)
}

func (r *WorkloadIdentityRecoveryReconciler) preflightRecovery(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
) (ctrl.Result, error) {
	issuer := &workloadidentityv1alpha1.OIDCIssuer{}
	if err := r.Get(ctx, types.NamespacedName{Name: workloadidentityv1alpha1.OIDCIssuerName}, issuer); err != nil {
		if apierrors.IsNotFound(err) {
			return r.block(ctx, recovery, recoveryReasonOIDCIssuerNotReady, "OIDCIssuer was not found")
		}
		return ctrl.Result{}, err
	}
	if !isOIDCIssuerReady(issuer) {
		return r.block(ctx, recovery, recoveryReasonOIDCIssuerNotReady, "OIDCIssuer is not ready")
	}
	plan, err := r.Manager.Inspect(
		ctx,
		recovery,
		identity,
		issuer.Status.IssuerURL,
		serviceAccountSubject(identity),
	)
	if err != nil {
		return r.handleRecoveryError(ctx, recovery, err)
	}
	if err := r.validateRecoveryServiceAccount(ctx, recovery, identity); err != nil {
		return r.handleRecoveryError(ctx, recovery, err)
	}
	if err := r.patchRecoveryStatus(ctx, recovery, func(
		status *workloadidentityv1alpha1.WorkloadIdentityRecoveryStatus,
	) {
		status.Plan = plan
		setRecoveryCondition(
			status,
			workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionProgressing,
			metav1.ConditionTrue,
			"PreflightComplete",
			"Recovery preflight completed without external mutation",
			recovery.Generation,
		)
		clearRecoveryCondition(status, workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionBlocked)
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

func (r *WorkloadIdentityRecoveryReconciler) startRecovery(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
) (ctrl.Result, error) {
	winner, err := r.recoveryWinnerForSource(ctx, recovery)
	if err != nil {
		return ctrl.Result{}, err
	}
	if winner != nil {
		return r.fail(
			ctx,
			recovery,
			recoveryReasonDuplicate,
			fmt.Sprintf(
				"WorkloadIdentityRecovery %q already owns recovery for previous WorkloadIdentity UID %q",
				winner.Name,
				recovery.Spec.PreviousWorkloadIdentityUID,
			),
		)
	}
	if err := r.markTargetRecoveryInProgress(ctx, recovery, identity); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.patchRecoveryStatus(ctx, recovery, func(
		status *workloadidentityv1alpha1.WorkloadIdentityRecoveryStatus,
	) {
		now := metav1.Now()
		status.MutationStarted = true
		status.StartedTime = &now
		setRecoveryCondition(
			status,
			workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionProgressing,
			metav1.ConditionTrue,
			"RecoveryStarted",
			"Forward-only recovery started",
			recovery.Generation,
		)
		clearRecoveryCondition(status, workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionBlocked)
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

func (r *WorkloadIdentityRecoveryReconciler) advanceRecovery(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
) (ctrl.Result, error) {
	if err := r.Manager.MarkInProgress(ctx, recovery, identity, recovery.Status.Plan); err != nil {
		return r.handleRecoveryError(ctx, recovery, err)
	}
	if err := r.Manager.EnsureFederatedIdentityCredential(
		ctx,
		recovery,
		identity,
		recovery.Status.Plan,
	); err != nil {
		return r.handleRecoveryError(ctx, recovery, err)
	}
	serviceAccountUID, err := r.transferRecoveryServiceAccount(ctx, recovery, identity, recovery.Status.Plan)
	if err != nil {
		return r.handleRecoveryError(ctx, recovery, err)
	}
	return r.commitRecovery(ctx, recovery, identity, serviceAccountUID)
}

func (r *WorkloadIdentityRecoveryReconciler) commitRecovery(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	serviceAccountUID types.UID,
) (ctrl.Result, error) {
	reader := r.recoveryReader()
	currentRecovery := &workloadidentityv1alpha1.WorkloadIdentityRecovery{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(recovery), currentRecovery); err != nil {
		return ctrl.Result{}, err
	}
	if currentRecovery.UID != recovery.UID {
		return ctrl.Result{}, fmt.Errorf("WorkloadIdentityRecovery instance changed before commit")
	}
	currentIdentity := &workloadidentityv1alpha1.WorkloadIdentity{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(identity), currentIdentity); err != nil {
		return r.handleRecoveryError(ctx, currentRecovery, err)
	}
	if err := validateRecoveryControllerTarget(currentRecovery, currentIdentity); err != nil {
		return r.block(ctx, currentRecovery, recoveryReasonTargetChanged, err.Error())
	}
	if err := r.verifyRecoveryServiceAccountForCommit(
		ctx,
		reader,
		currentIdentity,
		currentRecovery.Status.Plan,
		serviceAccountUID,
	); err != nil {
		return r.handleRecoveryError(ctx, currentRecovery, err)
	}
	if err := r.Manager.Commit(
		ctx,
		currentRecovery,
		currentIdentity,
		currentRecovery.Status.Plan,
	); err != nil {
		return r.handleRecoveryError(ctx, currentRecovery, err)
	}
	return r.recordRecoveryCommitVerified(ctx, currentRecovery)
}

func (r *WorkloadIdentityRecoveryReconciler) recordRecoveryCommitVerified(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
) (ctrl.Result, error) {
	if err := r.patchRecoveryStatus(ctx, recovery, func(
		status *workloadidentityv1alpha1.WorkloadIdentityRecoveryStatus,
	) {
		status.CommitVerified = true
		setRecoveryCondition(
			status,
			workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionProgressing,
			metav1.ConditionTrue,
			recoveryReasonCommitVerified,
			"Azure ownership commit was read-verified; finalizing recovery fence",
			recovery.Generation,
		)
		clearRecoveryCondition(status, workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionBlocked)
		clearRecoveryCondition(status, workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionComplete)
		clearRecoveryCondition(status, workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionFailed)
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

func (r *WorkloadIdentityRecoveryReconciler) finalizeRecovery(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
) (ctrl.Result, error) {
	if err := r.Manager.Finalize(ctx, recovery, identity, recovery.Status.Plan); err != nil {
		return r.handleRecoveryError(ctx, recovery, err)
	}
	if err := r.patchRecoveryStatus(ctx, recovery, func(
		status *workloadidentityv1alpha1.WorkloadIdentityRecoveryStatus,
	) {
		now := metav1.Now()
		status.CompletedTime = &now
		setRecoveryCondition(
			status,
			workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionComplete,
			metav1.ConditionTrue,
			recoveryReasonCompleted,
			"Forward recovery completed and the Azure fence was cleared",
			recovery.Generation,
		)
		setRecoveryCondition(
			status,
			workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionProgressing,
			metav1.ConditionFalse,
			recoveryReasonCompleted,
			"Recovery is complete",
			recovery.Generation,
		)
		clearRecoveryCondition(status, workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionBlocked)
		clearRecoveryCondition(status, workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionFailed)
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

func (r *WorkloadIdentityRecoveryReconciler) reconcileDelete(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(recovery, workloadIdentityRecoveryFinalizer) {
		return ctrl.Result{}, nil
	}
	if !recovery.Status.MutationStarted {
		winner, err := r.recoveryWinnerForSource(ctx, recovery)
		if err != nil {
			return ctrl.Result{}, err
		}
		if winner == nil {
			if err := r.releaseCancelledTarget(ctx, recovery); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, r.removeRecoveryFinalizer(ctx, recovery)
	}
	if recoveryIsComplete(recovery) {
		if err := r.releaseCompletedTarget(ctx, recovery); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.removeRecoveryFinalizer(ctx, recovery)
	}
	if r.Manager == nil {
		return r.block(
			ctx,
			recovery,
			recoveryReasonManagerNotConfigured,
			"Azure recovery manager is not configured",
		)
	}
	result, err := r.reconcileForward(ctx, recovery)
	if err != nil {
		return result, err
	}
	if recoveryIsComplete(recovery) {
		if err := r.releaseCompletedTarget(ctx, recovery); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.removeRecoveryFinalizer(ctx, recovery)
	}
	return result, nil
}

func (r *WorkloadIdentityRecoveryReconciler) removeRecoveryFinalizer(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
) error {
	current := &workloadidentityv1alpha1.WorkloadIdentityRecovery{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(recovery), current); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	controllerutil.RemoveFinalizer(current, workloadIdentityRecoveryFinalizer)
	return r.Update(ctx, current)
}

func (r *WorkloadIdentityRecoveryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&workloadidentityv1alpha1.WorkloadIdentityRecovery{},
		workloadIdentityRecoveryTargetIndex,
		activeRecoveryTargetIndexValues,
	); err != nil {
		return fmt.Errorf("index active WorkloadIdentityRecoveries by target reference: %w", err)
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(
			&workloadidentityv1alpha1.WorkloadIdentityRecovery{},
			builder.WithPredicates(primaryResourcePredicate()),
		).
		Watches(
			&workloadidentityv1alpha1.WorkloadIdentity{},
			handler.EnqueueRequestsFromMapFunc(r.recoveriesForWorkloadIdentity),
			builder.WithPredicates(workloadIdentityRecoveryTargetPredicate()),
		).
		WithOptions(controllerpkg.Options{MaxConcurrentReconciles: 1}).
		Named("workloadidentityrecovery").
		Complete(r)
}
