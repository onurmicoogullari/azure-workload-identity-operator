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
	"maps"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/az-workload-identity-operator/internal/workloadidentity"
)

const workloadIdentityFinalizer = "workloadidentity.azure.micosolutions.se/workloadidentity-finalizer"

const (
	serviceAccountUseLabel       = "azure.workload.identity/use"
	serviceAccountClientID       = "azure.workload.identity/client-id"
	serviceAccountTenantID       = "azure.workload.identity/tenant-id"
	serviceAccountManagedBy      = "workloadidentity.azure.micosolutions.se/managed-by"
	serviceAccountUID            = "workloadidentity.azure.micosolutions.se/workload-identity-uid"
	serviceAccountCreatedBy      = "workloadidentity.azure.micosolutions.se/created-by-operator"
	serviceAccountManagerName    = "az-workload-identity-operator"
	serviceAccountSubjectPattern = "system:serviceaccount:%s:%s"
	trueValue                    = "true"
)

// WorkloadIdentityReconciler reconciles a WorkloadIdentity object.
type WorkloadIdentityReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Manager workloadidentity.Manager
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
		return ctrl.Result{}, r.reconcileWorkloadIdentityDelete(ctx, identity)
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

	subject := serviceAccountSubject(identity)
	managed, err := r.Manager.Ensure(ctx, identity, issuer.Status.IssuerURL, subject)
	if err != nil {
		log.Error(err, "Failed to ensure Azure workload identity")
		statusErr := r.setWorkloadIdentityNotReady(ctx, identity, "AzureEnsureFailed", err.Error())
		if statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, err
	}

	if err := r.ensureServiceAccount(ctx, identity, managed); err != nil {
		log.Error(err, "Failed to ensure ServiceAccount")
		statusErr := r.setWorkloadIdentityNotReady(ctx, identity, "ServiceAccountEnsureFailed", err.Error())
		if statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.setWorkloadIdentityReady(ctx, identity, issuer.Status.IssuerURL, subject, managed)
}

func (r *WorkloadIdentityReconciler) reconcileWorkloadIdentityDelete(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) error {
	if !controllerutil.ContainsFinalizer(identity, workloadIdentityFinalizer) {
		return nil
	}

	if identity.Spec.DeletionPolicy == azworkloadidentityv1alpha1.DeletionPolicyDelete {
		if r.Manager == nil {
			return fmt.Errorf("azure workload identity manager is not configured")
		}
		if err := r.Manager.Delete(ctx, identity); err != nil {
			return err
		}
		if err := r.deleteServiceAccountIfOwned(ctx, identity); err != nil {
			return err
		}
	}

	controllerutil.RemoveFinalizer(identity, workloadIdentityFinalizer)
	return r.Update(ctx, identity)
}

func (r *WorkloadIdentityReconciler) ensureServiceAccount(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, managed workloadidentity.ManagedIdentity) error {
	key := serviceAccountKey(identity)
	serviceAccount := &corev1.ServiceAccount{}
	if err := r.Get(ctx, key, serviceAccount); apierrors.IsNotFound(err) {
		serviceAccount = &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
		serviceAccount.Labels = desiredServiceAccountLabels(identity, true)
		serviceAccount.Annotations = desiredServiceAccountAnnotations(managed)
		return r.Create(ctx, serviceAccount)
	} else if err != nil {
		return err
	}

	original := serviceAccount.DeepCopy()
	created := serviceAccount.Labels[serviceAccountCreatedBy] == trueValue && serviceAccount.Labels[serviceAccountUID] == string(identity.UID)
	serviceAccount.Labels = mergeStringMap(serviceAccount.Labels, desiredServiceAccountLabels(identity, created))
	serviceAccount.Annotations = mergeStringMap(serviceAccount.Annotations, desiredServiceAccountAnnotations(managed))
	return r.Patch(ctx, serviceAccount, client.MergeFrom(original))
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

func (r *WorkloadIdentityReconciler) setWorkloadIdentityReady(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, issuerURL, subject string, managed workloadidentity.ManagedIdentity) error {
	original := identity.DeepCopy()
	now := metav1.Now()
	identity.Status.ClientID = managed.ClientID
	identity.Status.PrincipalID = managed.PrincipalID
	identity.Status.TenantID = managed.TenantID
	identity.Status.IssuerURL = issuerURL
	identity.Status.Subject = subject
	identity.Status.AzureResources = managed.AzureResources
	identity.Status.ObservedGeneration = identity.Generation
	identity.Status.LastReconciledTime = &now
	apimeta.SetStatusCondition(&identity.Status.Conditions, metav1.Condition{
		Type:               string(azworkloadidentityv1alpha1.WorkloadIdentityConditionReady),
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            "Workload identity is reconciled",
		ObservedGeneration: identity.Generation,
	})
	return r.Status().Patch(ctx, identity, client.MergeFrom(original))
}

func (r *WorkloadIdentityReconciler) setWorkloadIdentityNotReady(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, reason, message string) error {
	original := identity.DeepCopy()
	now := metav1.Now()
	identity.Status.ObservedGeneration = identity.Generation
	identity.Status.LastReconciledTime = &now
	apimeta.SetStatusCondition(&identity.Status.Conditions, metav1.Condition{
		Type:               string(azworkloadidentityv1alpha1.WorkloadIdentityConditionReady),
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: identity.Generation,
	})
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
	return ctrl.NewControllerManagedBy(mgr).
		For(&azworkloadidentityv1alpha1.WorkloadIdentity{}).
		Watches(&azworkloadidentityv1alpha1.OIDCIssuer{}, handler.EnqueueRequestsFromMapFunc(r.workloadIdentitiesForOIDCIssuer)).
		Named("workloadidentity").
		Complete(r)
}
