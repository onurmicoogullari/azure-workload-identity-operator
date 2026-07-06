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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	Scheme          *runtime.Scheme
	Manager         workloadidentity.Manager
	RefreshInterval time.Duration
}

// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=workloadidentities,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=workloadidentities/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=workloadidentities/finalizers,verbs=update
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=oidcissuers,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete

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

	issuer := &azworkloadidentityv1alpha1.OIDCIssuer{}
	if err := r.Get(ctx, types.NamespacedName{Name: azworkloadidentityv1alpha1.OIDCIssuerName}, issuer); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setWorkloadIdentityNotReady(ctx, identity, "OIDCIssuerNotFound", fmt.Sprintf("OIDCIssuer %q was not found", azworkloadidentityv1alpha1.OIDCIssuerName))
		}
		return ctrl.Result{}, err
	}
	if !isOIDCIssuerReady(issuer) {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setWorkloadIdentityNotReady(ctx, identity, "OIDCIssuerNotReady", fmt.Sprintf("OIDCIssuer %q is not ready", azworkloadidentityv1alpha1.OIDCIssuerName))
	}

	if r.Manager == nil {
		return ctrl.Result{}, r.setWorkloadIdentityNotReady(ctx, identity, "ManagerNotConfigured", "Azure workload identity manager is not configured")
	}

	if err := r.validateExistingServiceAccount(ctx, identity); err != nil {
		log.Error(err, "Failed to validate existing ServiceAccount")
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
		log.Error(err, "Failed to ensure Azure workload identity")
		reason := "AzureEnsureFailed"
		if conflictReason, ok := workloadidentity.ConflictReason(err); ok {
			reason = conflictReason
		}
		statusErr := r.setWorkloadIdentityNotReady(ctx, identity, reason, err.Error())
		if statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, err
	}

	serviceAccount, err := r.ensureServiceAccount(ctx, identity, managed)
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

	if err := r.setWorkloadIdentityReady(ctx, identity, issuer.Status.IssuerURL, subject, string(serviceAccount.UID), managed); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: r.refreshInterval(identity)}, nil
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

	if identity.Spec.DeletionPolicy == azworkloadidentityv1alpha1.DeletionPolicyDelete {
		if r.Manager == nil {
			return ctrl.Result{}, fmt.Errorf("azure workload identity manager is not configured")
		}
		deleteOptions, waitForPeers, err := r.workloadIdentityDeleteOptions(ctx, identity)
		if err != nil {
			return ctrl.Result{}, err
		}
		if waitForPeers {
			logf.FromContext(ctx).Info(
				"Waiting for terminating WorkloadIdentities that share Azure parents",
				"resourceGroup", identity.Spec.Azure.ResourceGroupName,
				"userAssignedIdentity", identity.Spec.Azure.UserAssignedIdentityName,
				"preserveResourceGroup", deleteOptions.PreserveResourceGroup,
				"resourceGroupSuccessorUID", deleteOptions.ResourceGroupSuccessorUID,
				"preserveUserAssignedIdentity", deleteOptions.PreserveUserAssignedIdentity,
				"userAssignedIdentitySuccessorUID", deleteOptions.UserAssignedIdentitySuccessorUID,
			)
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		logf.FromContext(ctx).Info(
			"Deleting WorkloadIdentity Azure resources",
			"preserveResourceGroup", deleteOptions.PreserveResourceGroup,
			"resourceGroupSuccessorUID", deleteOptions.ResourceGroupSuccessorUID,
			"preserveUserAssignedIdentity", deleteOptions.PreserveUserAssignedIdentity,
			"userAssignedIdentitySuccessorUID", deleteOptions.UserAssignedIdentitySuccessorUID,
		)
		if dependencyAwareManager, ok := r.Manager.(workloadidentity.DependencyAwareManager); ok {
			err = dependencyAwareManager.DeleteWithOptions(ctx, identity, deleteOptions)
		} else {
			err = r.Manager.Delete(ctx, identity)
		}
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.deleteServiceAccountIfOwned(ctx, identity); err != nil {
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(identity, workloadIdentityFinalizer)
	return ctrl.Result{}, r.Update(ctx, identity)
}

type deletionReference struct {
	key      string
	uid      string
	deleting bool
}

func (r *WorkloadIdentityReconciler) workloadIdentityDeleteOptions(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) (workloadidentity.DeleteOptions, bool, error) {
	identities := &azworkloadidentityv1alpha1.WorkloadIdentityList{}
	if err := r.List(ctx, identities); err != nil {
		return workloadidentity.DeleteOptions{}, false, fmt.Errorf("list WorkloadIdentities before Azure deletion: %w", err)
	}

	resourceGroupReferences := make([]deletionReference, 0, len(identities.Items))
	userAssignedIdentityReferences := make([]deletionReference, 0, len(identities.Items))
	for i := range identities.Items {
		other := &identities.Items[i]
		if other.UID == identity.UID {
			continue
		}
		if !strings.EqualFold(other.Spec.Azure.SubscriptionID, identity.Spec.Azure.SubscriptionID) ||
			!strings.EqualFold(other.Spec.Azure.ResourceGroupName, identity.Spec.Azure.ResourceGroupName) {
			continue
		}
		reference := deletionReference{key: other.Namespace + "/" + other.Name, uid: string(other.UID), deleting: !other.DeletionTimestamp.IsZero()}
		resourceGroupReferences = append(resourceGroupReferences, reference)
		if strings.EqualFold(other.Spec.Azure.UserAssignedIdentityName, identity.Spec.Azure.UserAssignedIdentityName) {
			userAssignedIdentityReferences = append(userAssignedIdentityReferences, reference)
		}
	}

	current := deletionReference{key: identity.Namespace + "/" + identity.Name, uid: string(identity.UID), deleting: true}
	resourceGroupSuccessor, waitForResourceGroup := deletionSuccessor(current, resourceGroupReferences)
	userAssignedIdentitySuccessor, waitForUserAssignedIdentity := deletionSuccessor(current, userAssignedIdentityReferences)
	options := workloadidentity.DeleteOptions{
		PreserveResourceGroup:            len(resourceGroupReferences) > 0,
		PreserveUserAssignedIdentity:     len(userAssignedIdentityReferences) > 0,
		ResourceGroupSuccessorUID:        resourceGroupSuccessor,
		UserAssignedIdentitySuccessorUID: userAssignedIdentitySuccessor,
	}
	return options, waitForResourceGroup || waitForUserAssignedIdentity, nil
}

func deletionSuccessor(current deletionReference, references []deletionReference) (string, bool) {
	if len(references) == 0 {
		return "", false
	}
	successor := deletionReference{}
	for _, reference := range references {
		if reference.deleting {
			continue
		}
		if successor.key == "" || reference.key < successor.key {
			successor = reference
		}
	}
	if successor.key != "" {
		return successor.uid, false
	}

	coordinator := current
	for _, reference := range references {
		if reference.key < coordinator.key {
			coordinator = reference
		}
	}
	if coordinator.uid == current.uid {
		return "", true
	}
	return coordinator.uid, false
}

func (r *WorkloadIdentityReconciler) ensureServiceAccount(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, managed workloadidentity.ManagedIdentity) (*corev1.ServiceAccount, error) {
	key := serviceAccountKey(identity)
	serviceAccount := &corev1.ServiceAccount{}
	if err := r.Get(ctx, key, serviceAccount); apierrors.IsNotFound(err) {
		serviceAccount = &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
		serviceAccount.Labels = desiredServiceAccountLabels(identity, true)
		serviceAccount.Annotations = desiredServiceAccountAnnotations(managed)
		if createErr := r.Create(ctx, serviceAccount); createErr != nil {
			return nil, createErr
		}
		return serviceAccount, nil
	} else if err != nil {
		return nil, err
	}

	if err := validateServiceAccountOwnership(identity, serviceAccount); err != nil {
		return nil, err
	}
	if err := validateServiceAccountAnnotations(identity, serviceAccount, managed); err != nil {
		return nil, err
	}

	original := serviceAccount.DeepCopy()
	created := serviceAccount.Labels[serviceAccountCreatedBy] == trueValue && serviceAccount.Labels[serviceAccountUID] == string(identity.UID)
	desiredLabels := mergeStringMap(serviceAccount.Labels, desiredServiceAccountLabels(identity, created))
	desiredAnnotations := mergeStringMap(serviceAccount.Annotations, desiredServiceAccountAnnotations(managed))
	if maps.Equal(serviceAccount.Labels, desiredLabels) && maps.Equal(serviceAccount.Annotations, desiredAnnotations) {
		return serviceAccount, nil
	}
	serviceAccount.Labels = desiredLabels
	serviceAccount.Annotations = desiredAnnotations
	if err := r.Patch(ctx, serviceAccount, client.MergeFrom(original)); err != nil {
		return nil, err
	}
	return serviceAccount, nil
}

func (r *WorkloadIdentityReconciler) validateExistingServiceAccount(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) error {
	serviceAccount := &corev1.ServiceAccount{}
	if err := r.Get(ctx, serviceAccountKey(identity), serviceAccount); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := validateServiceAccountOwnership(identity, serviceAccount); err != nil {
		return err
	}
	if identity.Status.ServiceAccountUID != "" && identity.Status.ServiceAccountUID != string(serviceAccount.UID) {
		return newServiceAccountConflict(
			"ServiceAccount %q was replaced after reconciliation; expected UID %q, got %q",
			client.ObjectKeyFromObject(serviceAccount).String(),
			identity.Status.ServiceAccountUID,
			serviceAccount.UID,
		)
	}
	return validateUnownedServiceAccountAnnotations(identity, serviceAccount)
}

type serviceAccountConflictError struct {
	message string
}

func (e *serviceAccountConflictError) Error() string {
	return e.message
}

func newServiceAccountConflict(format string, args ...any) error {
	return &serviceAccountConflictError{message: fmt.Sprintf(format, args...)}
}

func isServiceAccountConflict(err error) bool {
	conflict := &serviceAccountConflictError{}
	return errors.As(err, &conflict)
}

func validateServiceAccountOwnership(identity *azworkloadidentityv1alpha1.WorkloadIdentity, serviceAccount *corev1.ServiceAccount) error {
	ownerUID := serviceAccount.Labels[serviceAccountUID]
	if ownerUID != "" && ownerUID != string(identity.UID) {
		return newServiceAccountConflict(
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

func validateServiceAccountAnnotations(identity *azworkloadidentityv1alpha1.WorkloadIdentity, serviceAccount *corev1.ServiceAccount, managed workloadidentity.ManagedIdentity) error {
	if serviceAccountOwnedBy(identity, serviceAccount) {
		return nil
	}

	if existing := serviceAccount.Annotations[serviceAccountClientID]; existing != "" && existing != managed.ClientID {
		return newServiceAccountConflict(
			"ServiceAccount %q is already annotated for Azure client ID %q",
			client.ObjectKeyFromObject(serviceAccount).String(),
			existing,
		)
	}
	if existing := serviceAccount.Annotations[serviceAccountTenantID]; existing != "" && existing != managed.TenantID {
		return newServiceAccountConflict(
			"ServiceAccount %q is already annotated for Azure tenant ID %q",
			client.ObjectKeyFromObject(serviceAccount).String(),
			existing,
		)
	}
	return nil
}

func validateUnownedServiceAccountAnnotations(identity *azworkloadidentityv1alpha1.WorkloadIdentity, serviceAccount *corev1.ServiceAccount) error {
	if serviceAccountOwnedBy(identity, serviceAccount) {
		return nil
	}

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

func (r *WorkloadIdentityReconciler) deleteServiceAccountIfOwned(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) error {
	serviceAccount := &corev1.ServiceAccount{}
	if err := r.Get(ctx, serviceAccountKey(identity), serviceAccount); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	if serviceAccount.Labels[serviceAccountCreatedBy] != trueValue || serviceAccount.Labels[serviceAccountUID] != string(identity.UID) {
		return nil
	}
	return r.Delete(ctx, serviceAccount)
}

func (r *WorkloadIdentityReconciler) setWorkloadIdentityReady(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, issuerURL, subject, serviceAccountUID string, managed workloadidentity.ManagedIdentity) error {
	return r.patchWorkloadIdentityStatus(ctx, identity, func(status *azworkloadidentityv1alpha1.WorkloadIdentityStatus) {
		status.ClientID = managed.ClientID
		status.PrincipalID = managed.PrincipalID
		status.TenantID = managed.TenantID
		status.IssuerURL = issuerURL
		status.Subject = subject
		status.ServiceAccountUID = serviceAccountUID
		status.AzureResources = managed.AzureResources
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
		apimeta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               string(azworkloadidentityv1alpha1.WorkloadIdentityConditionReady),
			Status:             metav1.ConditionFalse,
			Reason:             reason,
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
	return r.Status().Patch(ctx, identity, client.MergeFrom(original))
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
		For(&azworkloadidentityv1alpha1.WorkloadIdentity{}, builder.WithPredicates(primaryResourcePredicate())).
		Watches(&azworkloadidentityv1alpha1.OIDCIssuer{}, handler.EnqueueRequestsFromMapFunc(r.workloadIdentitiesForOIDCIssuer), builder.WithPredicates(oidcIssuerDependencyPredicate())).
		Watches(&corev1.ServiceAccount{}, handler.EnqueueRequestsFromMapFunc(r.workloadIdentitiesForServiceAccount), builder.WithPredicates(serviceAccountDependencyPredicate())).
		Named("workloadidentity").
		Complete(r)
}
