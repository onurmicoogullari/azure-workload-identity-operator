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
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

const (
	workloadIdentityFinalizer               = "workloadidentity.azure.micosolutions.se/workloadidentity-finalizer"
	workloadIdentityServiceAccountNameIndex = "workloadidentity.spec.serviceAccount.name"
	workloadIdentityUIDIndex                = "workloadidentity.metadata.uid"
	workloadIdentityRecoveryPollInterval    = 5 * time.Second
)

const (
	// DefaultWorkloadIdentityRefreshInterval is the default interval for revalidating Azure resources and ServiceAccounts.
	DefaultWorkloadIdentityRefreshInterval = 5 * time.Minute
	serviceAccountUseLabel                 = "azure.workload.identity/use"
	serviceAccountClientID                 = "azure.workload.identity/client-id"
	serviceAccountTenantID                 = "azure.workload.identity/tenant-id"
	serviceAccountManagedBy                = "workloadidentity.azure.micosolutions.se/managed-by"
	serviceAccountUID                      = "workloadidentity.azure.micosolutions.se/workload-identity-uid"
	serviceAccountCreatedBy                = "workloadidentity.azure.micosolutions.se/created-by-operator"
	serviceAccountManagerName              = "azure-workload-identity-operator"
	serviceAccountSubjectPattern           = "system:serviceaccount:%s:%s"
	trueValue                              = "true"
)

// WorkloadIdentityReconciler reconciles a WorkloadIdentity object.
type WorkloadIdentityReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Manager          workloadidentity.Manager
	RecoveryDetector workloadidentity.RecoveryDetector
	Recorder         events.EventRecorder
	RefreshInterval  time.Duration
}

// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=workloadidentities,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=workloadidentities/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=workloadidentities/finalizers,verbs=update
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=oidcissuers,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

func (r *WorkloadIdentityReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	identity := &azworkloadidentityv1alpha1.WorkloadIdentity{}
	if err := r.Get(ctx, req.NamespacedName, identity); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !identity.DeletionTimestamp.IsZero() {
		return r.reconcileWorkloadIdentityDelete(ctx, identity)
	}

	if !controllerutil.ContainsFinalizer(identity, workloadIdentityFinalizer) {
		controllerutil.AddFinalizer(identity, workloadIdentityFinalizer)
		if err := r.Update(ctx, identity); err != nil {
			return ctrl.Result{}, err
		}
	}

	if workloadIdentityRecoveryIsInProgress(identity) {
		return ctrl.Result{RequeueAfter: workloadIdentityRecoveryPollInterval}, nil
	}

	issuer := &azworkloadidentityv1alpha1.OIDCIssuer{}
	if err := r.Get(ctx, types.NamespacedName{Name: azworkloadidentityv1alpha1.OIDCIssuerName}, issuer); err != nil {
		if apierrors.IsNotFound(err) {
			return r.waitForOIDCIssuer(
				ctx,
				identity,
				"OIDCIssuerNotFound",
				fmt.Sprintf("OIDCIssuer %q was not found", azworkloadidentityv1alpha1.OIDCIssuerName),
			)
		}
		return ctrl.Result{}, err
	}
	if !isOIDCIssuerReady(issuer) {
		return r.waitForOIDCIssuer(
			ctx,
			identity,
			"OIDCIssuerNotReady",
			fmt.Sprintf("OIDCIssuer %q is not ready", azworkloadidentityv1alpha1.OIDCIssuerName),
		)
	}

	if r.Manager == nil {
		return ctrl.Result{}, r.setWorkloadIdentityNotReady(ctx, identity, "ManagerNotConfigured", "Azure workload identity manager is not configured")
	}

	serviceAccountState, err := r.inspectServiceAccount(ctx, identity)
	if err != nil {
		log.Error(err, "Failed to validate existing ServiceAccount")
		if conflictUID, ok := serviceAccountConflictOwnerUID(err); ok && r.RecoveryDetector != nil {
			evidence, detectionErr := r.RecoveryDetector.DetectRecovery(ctx, identity)
			if detectionErr == nil && evidence.PreviousWorkloadIdentityUID == types.UID(conflictUID) {
				message := fmt.Sprintf(
					"WorkloadIdentity %s/%s and ServiceAccount %s/%s belong to earlier WorkloadIdentity UID %q; recovery is required",
					identity.Namespace,
					identity.Name,
					identity.Namespace,
					identity.Spec.ServiceAccount.Name,
					conflictUID,
				)
				return ctrl.Result{}, r.setWorkloadIdentityRecoveryRequired(ctx, identity, evidence, message)
			}
			if reason, ok := workloadidentity.ConflictReason(detectionErr); ok &&
				reason == workloadidentity.ReasonRecoveryInProgress {
				return ctrl.Result{RequeueAfter: workloadIdentityRecoveryPollInterval}, r.setWorkloadIdentityNotReady(
					ctx,
					identity,
					workloadidentity.ReasonRecoveryInProgress,
					detectionErr.Error(),
				)
			}
		}
		reason := "ServiceAccountReadFailed"
		if isServiceAccountConflict(err) {
			reason = "ServiceAccountConflict"
		}
		statusErr := r.setWorkloadIdentityNotReady(ctx, identity, reason, err.Error())
		if statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, err
	}

	subject := serviceAccountSubject(identity)
	managed, err := r.Manager.Ensure(ctx, identity, issuer.Status.IssuerURL, subject)
	if err != nil {
		return r.handleWorkloadIdentityEnsureError(ctx, identity, err)
	}

	serviceAccount, serviceAccountProvenance, err := r.ensureServiceAccount(ctx, identity, managed, serviceAccountState)
	if err != nil {
		log.Error(err, "Failed to ensure ServiceAccount")
		reason := "ServiceAccountEnsureFailed"
		if isServiceAccountConflict(err) {
			reason = "ServiceAccountConflict"
		}
		statusErr := r.setWorkloadIdentityNotReady(ctx, identity, reason, err.Error())
		if statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, err
	}

	if err := r.setWorkloadIdentityReady(ctx, identity, issuer.Status.IssuerURL, subject, string(serviceAccount.UID), serviceAccountProvenance, managed); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: r.refreshInterval(identity)}, nil
}

func (r *WorkloadIdentityReconciler) waitForOIDCIssuer(
	ctx context.Context,
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
	reason, message string,
) (ctrl.Result, error) {
	result := ctrl.Result{RequeueAfter: 30 * time.Second}
	if workloadIdentityRecoveryStateIsActive(identity) {
		return result, nil
	}
	return result, r.setWorkloadIdentityNotReady(ctx, identity, reason, message)
}

func (r *WorkloadIdentityReconciler) handleWorkloadIdentityEnsureError(
	ctx context.Context,
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
	err error,
) (ctrl.Result, error) {
	if evidence, ok := workloadidentity.RecoveryRequiredDetails(err); ok {
		if statusErr := r.setWorkloadIdentityRecoveryRequired(ctx, identity, evidence, err.Error()); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: r.refreshInterval(identity)}, nil
	}
	if conflictReason, ok := workloadidentity.ConflictReason(err); ok &&
		conflictReason == workloadidentity.ReasonRecoveryInProgress {
		if statusErr := r.setWorkloadIdentityNotReady(
			ctx,
			identity,
			workloadidentity.ReasonRecoveryInProgress,
			err.Error(),
		); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: workloadIdentityRecoveryPollInterval}, nil
	}

	logf.FromContext(ctx).Error(err, "Failed to ensure Azure workload identity")
	reason := "AzureEnsureFailed"
	if conflictReason, ok := workloadidentity.ConflictReason(err); ok {
		reason = conflictReason
	}
	if statusErr := r.setWorkloadIdentityNotReady(ctx, identity, reason, err.Error()); statusErr != nil {
		return ctrl.Result{}, statusErr
	}
	return ctrl.Result{}, err
}

func (r *WorkloadIdentityReconciler) refreshInterval(identity *azworkloadidentityv1alpha1.WorkloadIdentity) time.Duration {
	interval := DefaultWorkloadIdentityRefreshInterval
	if r.RefreshInterval > 0 {
		interval = r.RefreshInterval
	}
	return jitteredWorkloadIdentityRefreshInterval(interval, string(identity.UID))
}

func jitteredWorkloadIdentityRefreshInterval(interval time.Duration, key string) time.Duration {
	if interval <= 0 || key == "" {
		return interval
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(key))
	maxJitter := interval / 10
	maxHash := uint64(^uint32(0))
	hashValue := uint64(hash.Sum32())
	jitterNanos := uint64(maxJitter)/maxHash*hashValue + uint64(maxJitter)%maxHash*hashValue/maxHash
	jitter := time.Duration(jitterNanos)
	return interval + jitter
}

func (r *WorkloadIdentityReconciler) reconcileWorkloadIdentityDelete(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(identity, workloadIdentityFinalizer) {
		return ctrl.Result{}, nil
	}
	if workloadIdentityRecoveryIsInProgress(identity) {
		return ctrl.Result{RequeueAfter: workloadIdentityRecoveryPollInterval}, nil
	}

	if identity.Spec.DeletionPolicy == azworkloadidentityv1alpha1.DeletionPolicyDelete {
		if r.Manager == nil {
			return ctrl.Result{}, fmt.Errorf("azure workload identity manager is not configured")
		}
		err := r.Manager.Delete(ctx, identity)
		if err != nil {
			if reason, ok := workloadidentity.ConflictReason(err); ok &&
				reason == workloadidentity.ReasonRecoveryRequired {
				r.emitRecoveryRequiredDeletionWarning(identity, err)
				controllerutil.RemoveFinalizer(identity, workloadIdentityFinalizer)
				return ctrl.Result{}, r.Update(ctx, identity)
			}
			return ctrl.Result{}, err
		}
		if err := r.deleteServiceAccountIfOwned(ctx, identity); err != nil {
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(identity, workloadIdentityFinalizer)
	return ctrl.Result{}, r.Update(ctx, identity)
}

func workloadIdentityRecoveryIsInProgress(
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
) bool {
	return workloadIdentityRecoveryReason(identity) == workloadidentity.ReasonRecoveryInProgress
}

func workloadIdentityRecoveryStateIsActive(
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
) bool {
	reason := workloadIdentityRecoveryReason(identity)
	return reason == workloadidentity.ReasonRecoveryRequired ||
		reason == workloadidentity.ReasonRecoveryInProgress
}

func workloadIdentityRecoveryReason(
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
) string {
	if identity.Status.Recovery == nil {
		return ""
	}
	ready := apimeta.FindStatusCondition(
		identity.Status.Conditions,
		string(azworkloadidentityv1alpha1.WorkloadIdentityConditionReady),
	)
	if ready == nil || ready.Status != metav1.ConditionFalse {
		return ""
	}
	return ready.Reason
}

func (r *WorkloadIdentityReconciler) emitRecoveryRequiredDeletionWarning(
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
	err error,
) {
	if r.Recorder != nil {
		r.Recorder.Eventf(
			identity,
			nil,
			corev1.EventTypeWarning,
			workloadidentity.ReasonRecoveryRequired,
			"PreserveAzureResources",
			"Preserved Azure resources because they belong to an earlier instance of this WorkloadIdentity",
		)
	}
	logf.Log.WithName("workloadidentity-controller").Error(
		err,
		"Preserved Azure resources while deleting recreated WorkloadIdentity",
		"namespace",
		identity.Namespace,
		"name",
		identity.Name,
	)
}

type serviceAccountReconcileState struct {
	Provenance  azworkloadidentityv1alpha1.ServiceAccountProvenance
	Established bool
}

func (r *WorkloadIdentityReconciler) ensureServiceAccount(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, managed workloadidentity.ManagedIdentity, state serviceAccountReconcileState) (*corev1.ServiceAccount, azworkloadidentityv1alpha1.ServiceAccountProvenance, error) {
	key := serviceAccountKey(identity)
	serviceAccount := &corev1.ServiceAccount{}
	provenance := state.Provenance
	if err := r.Get(ctx, key, serviceAccount); apierrors.IsNotFound(err) {
		if !state.Established {
			provenance = azworkloadidentityv1alpha1.ServiceAccountProvenanceCreated
		}
		serviceAccount = &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
		serviceAccount.Labels = desiredServiceAccountLabels(identity, provenance == azworkloadidentityv1alpha1.ServiceAccountProvenanceCreated)
		serviceAccount.Annotations = desiredServiceAccountAnnotations(managed)
		if createErr := r.Create(ctx, serviceAccount); createErr != nil {
			return nil, "", createErr
		}
		return serviceAccount, provenance, nil
	} else if err != nil {
		return nil, "", err
	}

	if err := validateServiceAccountOwnership(identity, serviceAccount); err != nil {
		return nil, "", err
	}
	if !state.Established {
		if err := validateUnownedServiceAccountAnnotations(serviceAccount); err != nil {
			return nil, "", err
		}
		provenance = azworkloadidentityv1alpha1.ServiceAccountProvenanceAdopted
	}

	original := serviceAccount.DeepCopy()
	created := provenance == azworkloadidentityv1alpha1.ServiceAccountProvenanceCreated
	desiredLabels := mergeStringMap(serviceAccount.Labels, desiredServiceAccountLabels(identity, created))
	desiredAnnotations := mergeStringMap(serviceAccount.Annotations, desiredServiceAccountAnnotations(managed))
	if maps.Equal(serviceAccount.Labels, desiredLabels) && maps.Equal(serviceAccount.Annotations, desiredAnnotations) {
		return serviceAccount, provenance, nil
	}
	serviceAccount.Labels = desiredLabels
	serviceAccount.Annotations = desiredAnnotations
	if err := r.Patch(ctx, serviceAccount, client.MergeFrom(original)); err != nil {
		return nil, "", err
	}
	return serviceAccount, provenance, nil
}

func (r *WorkloadIdentityReconciler) inspectServiceAccount(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) (serviceAccountReconcileState, error) {
	serviceAccount := &corev1.ServiceAccount{}
	if err := r.Get(ctx, serviceAccountKey(identity), serviceAccount); apierrors.IsNotFound(err) {
		if provenance, ok := persistedServiceAccountProvenance(identity); ok {
			return serviceAccountReconcileState{Provenance: provenance, Established: true}, nil
		}
		if serviceAccountStatusMatchesConfiguredName(identity) && identity.Status.ServiceAccountUID != "" {
			return serviceAccountReconcileState{
				Provenance:  azworkloadidentityv1alpha1.ServiceAccountProvenanceAdopted,
				Established: true,
			}, nil
		}
		return serviceAccountReconcileState{}, nil
	} else if err != nil {
		return serviceAccountReconcileState{}, err
	}
	if err := validateServiceAccountOwnership(identity, serviceAccount); err != nil {
		return serviceAccountReconcileState{}, err
	}
	if provenance, ok := persistedServiceAccountProvenance(identity); ok {
		return serviceAccountReconcileState{Provenance: provenance, Established: true}, nil
	}
	if (serviceAccountStatusMatchesConfiguredName(identity) && identity.Status.ServiceAccountUID != "") ||
		serviceAccountOwnedBy(identity, serviceAccount) {
		return serviceAccountReconcileState{
			Provenance:  serviceAccountProvenanceFromLabels(identity, serviceAccount),
			Established: true,
		}, nil
	}
	if err := validateUnownedServiceAccountAnnotations(serviceAccount); err != nil {
		return serviceAccountReconcileState{}, err
	}
	return serviceAccountReconcileState{}, nil
}

type serviceAccountConflictError struct {
	message  string
	ownerUID string
}

func (e *serviceAccountConflictError) Error() string {
	return e.message
}

func newServiceAccountConflict(format string, args ...any) error {
	return &serviceAccountConflictError{message: fmt.Sprintf(format, args...)}
}

func newServiceAccountOwnerConflict(ownerUID, format string, args ...any) error {
	return &serviceAccountConflictError{
		message:  fmt.Sprintf(format, args...),
		ownerUID: ownerUID,
	}
}

func isServiceAccountConflict(err error) bool {
	conflict := &serviceAccountConflictError{}
	return errors.As(err, &conflict)
}

func serviceAccountConflictOwnerUID(err error) (string, bool) {
	conflict := &serviceAccountConflictError{}
	if !errors.As(err, &conflict) || conflict.ownerUID == "" {
		return "", false
	}
	return conflict.ownerUID, true
}

func validateServiceAccountOwnership(identity *azworkloadidentityv1alpha1.WorkloadIdentity, serviceAccount *corev1.ServiceAccount) error {
	ownerUID := serviceAccount.Labels[serviceAccountUID]
	if ownerUID != "" && ownerUID != string(identity.UID) {
		return newServiceAccountOwnerConflict(
			ownerUID,
			"ServiceAccount %q is already managed by another WorkloadIdentity",
			client.ObjectKeyFromObject(serviceAccount).String(),
		)
	}
	if serviceAccount.Labels[serviceAccountManagedBy] == serviceAccountManagerName && ownerUID == "" {
		return newServiceAccountConflict(
			"ServiceAccount %q is managed by this operator but does not declare a WorkloadIdentity owner",
			client.ObjectKeyFromObject(serviceAccount).String(),
		)
	}
	return nil
}

func validateUnownedServiceAccountAnnotations(serviceAccount *corev1.ServiceAccount) error {
	if existing := serviceAccount.Annotations[serviceAccountClientID]; existing != "" {
		return newServiceAccountConflict(
			"ServiceAccount %q is already annotated for Azure client ID %q",
			client.ObjectKeyFromObject(serviceAccount).String(),
			existing,
		)
	}
	if existing := serviceAccount.Annotations[serviceAccountTenantID]; existing != "" {
		return newServiceAccountConflict(
			"ServiceAccount %q is already annotated for Azure tenant ID %q",
			client.ObjectKeyFromObject(serviceAccount).String(),
			existing,
		)
	}
	return nil
}

func serviceAccountOwnedBy(identity *azworkloadidentityv1alpha1.WorkloadIdentity, serviceAccount *corev1.ServiceAccount) bool {
	return serviceAccount.Labels[serviceAccountUID] == string(identity.UID)
}

func persistedServiceAccountProvenance(identity *azworkloadidentityv1alpha1.WorkloadIdentity) (azworkloadidentityv1alpha1.ServiceAccountProvenance, bool) {
	if !serviceAccountStatusMatchesConfiguredName(identity) {
		return "", false
	}
	switch identity.Status.ServiceAccountProvenance {
	case azworkloadidentityv1alpha1.ServiceAccountProvenanceCreated, azworkloadidentityv1alpha1.ServiceAccountProvenanceAdopted:
		return identity.Status.ServiceAccountProvenance, true
	default:
		return "", false
	}
}

func serviceAccountStatusMatchesConfiguredName(identity *azworkloadidentityv1alpha1.WorkloadIdentity) bool {
	return identity.Status.Subject != "" && identity.Status.Subject == serviceAccountSubject(identity)
}

func serviceAccountProvenanceFromLabels(identity *azworkloadidentityv1alpha1.WorkloadIdentity, serviceAccount *corev1.ServiceAccount) azworkloadidentityv1alpha1.ServiceAccountProvenance {
	if serviceAccount.Labels[serviceAccountCreatedBy] == trueValue && serviceAccountOwnedBy(identity, serviceAccount) {
		return azworkloadidentityv1alpha1.ServiceAccountProvenanceCreated
	}
	return azworkloadidentityv1alpha1.ServiceAccountProvenanceAdopted
}

func (r *WorkloadIdentityReconciler) deleteServiceAccountIfOwned(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) error {
	serviceAccount := &corev1.ServiceAccount{}
	if err := r.Get(ctx, serviceAccountKey(identity), serviceAccount); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	provenance, recorded := persistedServiceAccountProvenance(identity)
	if recorded && provenance != azworkloadidentityv1alpha1.ServiceAccountProvenanceCreated {
		return nil
	}
	if !recorded &&
		(serviceAccount.Labels[serviceAccountCreatedBy] != trueValue ||
			serviceAccount.Labels[serviceAccountUID] != string(identity.UID)) {
		return nil
	}
	return r.Delete(ctx, serviceAccount)
}

func (r *WorkloadIdentityReconciler) setWorkloadIdentityReady(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, issuerURL, subject, serviceAccountUID string, serviceAccountProvenance azworkloadidentityv1alpha1.ServiceAccountProvenance, managed workloadidentity.ManagedIdentity) error {
	return r.patchWorkloadIdentityStatus(ctx, identity, func(status *azworkloadidentityv1alpha1.WorkloadIdentityStatus) {
		status.ClientID = managed.ClientID
		status.PrincipalID = managed.PrincipalID
		status.TenantID = managed.TenantID
		status.IssuerURL = issuerURL
		status.Subject = subject
		status.ServiceAccountUID = serviceAccountUID
		status.ServiceAccountProvenance = serviceAccountProvenance
		status.AzureResources = managed.AzureResources
		status.Recovery = nil
		apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               string(azworkloadidentityv1alpha1.WorkloadIdentityConditionReady),
			Status:             metav1.ConditionTrue,
			Reason:             "Reconciled",
			Message:            "Workload identity is reconciled",
			ObservedGeneration: identity.Generation,
		})
	})
}

func (r *WorkloadIdentityReconciler) setWorkloadIdentityNotReady(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, reason, message string) error {
	return r.patchWorkloadIdentityStatus(ctx, identity, func(status *azworkloadidentityv1alpha1.WorkloadIdentityStatus) {
		if reason != workloadidentity.ReasonRecoveryInProgress {
			status.Recovery = nil
		}
		apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               string(azworkloadidentityv1alpha1.WorkloadIdentityConditionReady),
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: identity.Generation,
		})
	})
}

func (r *WorkloadIdentityReconciler) setWorkloadIdentityRecoveryRequired(
	ctx context.Context,
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
	evidence workloadidentity.RecoveryRequiredEvidence,
	message string,
) error {
	return r.patchWorkloadIdentityStatus(ctx, identity, func(status *azworkloadidentityv1alpha1.WorkloadIdentityStatus) {
		status.Recovery = &azworkloadidentityv1alpha1.WorkloadIdentityRecoveryRequiredStatus{
			PreviousWorkloadIdentityUID: evidence.PreviousWorkloadIdentityUID,
		}
		apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               string(azworkloadidentityv1alpha1.WorkloadIdentityConditionReady),
			Status:             metav1.ConditionFalse,
			Reason:             workloadidentity.ReasonRecoveryRequired,
			Message:            message,
			ObservedGeneration: identity.Generation,
		})
	})
}

func (r *WorkloadIdentityReconciler) patchWorkloadIdentityStatus(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, mutate func(*azworkloadidentityv1alpha1.WorkloadIdentityStatus)) error {
	original := identity.DeepCopy()
	now := metav1.Now()
	identity.Status.ObservedGeneration = identity.Generation
	identity.Status.LastReconciledTime = &now
	mutate(&identity.Status)
	return r.Status().Patch(
		ctx,
		identity,
		client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}),
	)
}

func isOIDCIssuerReady(issuer *azworkloadidentityv1alpha1.OIDCIssuer) bool {
	condition := apimeta.FindStatusCondition(issuer.Status.Conditions, string(azworkloadidentityv1alpha1.OIDCIssuerConditionReady))
	return condition != nil && condition.Status == metav1.ConditionTrue && issuer.Status.IssuerURL != ""
}

func (r *WorkloadIdentityReconciler) workloadIdentitiesForOIDCIssuer(ctx context.Context, object client.Object) []reconcile.Request {
	if object.GetName() != azworkloadidentityv1alpha1.OIDCIssuerName {
		return nil
	}

	identities := &azworkloadidentityv1alpha1.WorkloadIdentityList{}
	if err := r.List(ctx, identities); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list WorkloadIdentities for OIDCIssuer watch")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(identities.Items))
	for _, identity := range identities.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: identity.Name, Namespace: identity.Namespace}})
	}
	return requests
}

func (r *WorkloadIdentityReconciler) workloadIdentitiesForServiceAccount(ctx context.Context, object client.Object) []reconcile.Request {
	if object.GetNamespace() == "" || object.GetName() == "" {
		return nil
	}

	requests := make([]reconcile.Request, 0, 1)
	seen := map[types.NamespacedName]struct{}{}
	appendMatches := func(field, value string) bool {
		if value == "" {
			return true
		}
		identities := &azworkloadidentityv1alpha1.WorkloadIdentityList{}
		if err := r.List(ctx, identities, client.InNamespace(object.GetNamespace()), client.MatchingFields{field: value}); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to list indexed WorkloadIdentities for ServiceAccount watch", "field", field, "value", value)
			return false
		}
		for _, identity := range identities.Items {
			key := types.NamespacedName{Name: identity.Name, Namespace: identity.Namespace}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			requests = append(requests, reconcile.Request{NamespacedName: key})
		}
		return true
	}

	if !appendMatches(workloadIdentityServiceAccountNameIndex, object.GetName()) {
		return nil
	}
	ownerUID := object.GetLabels()[serviceAccountUID]
	if !appendMatches(workloadIdentityUIDIndex, ownerUID) {
		return nil
	}
	return requests
}

func serviceAccountKey(identity *azworkloadidentityv1alpha1.WorkloadIdentity) types.NamespacedName {
	return types.NamespacedName{Name: identity.Spec.ServiceAccount.Name, Namespace: identity.Namespace}
}

func serviceAccountSubject(identity *azworkloadidentityv1alpha1.WorkloadIdentity) string {
	key := serviceAccountKey(identity)
	return fmt.Sprintf(serviceAccountSubjectPattern, key.Namespace, key.Name)
}

func desiredServiceAccountLabels(identity *azworkloadidentityv1alpha1.WorkloadIdentity, createdByOperator bool) map[string]string {
	return map[string]string{
		serviceAccountUseLabel:  trueValue,
		serviceAccountManagedBy: serviceAccountManagerName,
		serviceAccountUID:       string(identity.UID),
		serviceAccountCreatedBy: fmt.Sprintf("%t", createdByOperator),
	}
}

func desiredServiceAccountAnnotations(managed workloadidentity.ManagedIdentity) map[string]string {
	annotations := map[string]string{
		serviceAccountClientID: managed.ClientID,
	}
	if managed.TenantID != "" {
		annotations[serviceAccountTenantID] = managed.TenantID
	}
	return annotations
}

func mergeStringMap(existing, desired map[string]string) map[string]string {
	merged := make(map[string]string, len(existing)+len(desired))
	maps.Copy(merged, existing)
	maps.Copy(merged, desired)
	return merged
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadIdentityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &azworkloadidentityv1alpha1.WorkloadIdentity{}, workloadIdentityServiceAccountNameIndex, func(object client.Object) []string {
		identity := object.(*azworkloadidentityv1alpha1.WorkloadIdentity)
		return []string{identity.Spec.ServiceAccount.Name}
	}); err != nil {
		return fmt.Errorf("index WorkloadIdentities by ServiceAccount name: %w", err)
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &azworkloadidentityv1alpha1.WorkloadIdentity{}, workloadIdentityUIDIndex, func(object client.Object) []string {
		return []string{string(object.GetUID())}
	}); err != nil {
		return fmt.Errorf("index WorkloadIdentities by UID: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&azworkloadidentityv1alpha1.WorkloadIdentity{}, builder.WithPredicates(workloadIdentityPrimaryPredicate())).
		Watches(&azworkloadidentityv1alpha1.OIDCIssuer{}, handler.EnqueueRequestsFromMapFunc(r.workloadIdentitiesForOIDCIssuer), builder.WithPredicates(oidcIssuerDependencyPredicate())).
		Watches(&corev1.ServiceAccount{}, handler.EnqueueRequestsFromMapFunc(r.workloadIdentitiesForServiceAccount), builder.WithPredicates(serviceAccountDependencyPredicate())).
		Named("workloadidentity").
		Complete(r)
}
