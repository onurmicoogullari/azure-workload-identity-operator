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

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
	"github.com/onurmicoogullari/az-workload-identity-operator/internal/oidc"
	"github.com/onurmicoogullari/az-workload-identity-operator/internal/oidcissuer"
	"github.com/onurmicoogullari/az-workload-identity-operator/internal/workloadidentity"
)

const oidcIssuerFinalizer = "workloadidentity.azure.micosolutions.se/oidcissuer-finalizer"

const (
	defaultSigningKeyRefreshInterval       = 5 * time.Minute
	defaultServiceAccountIssuerCheckPeriod = time.Minute
	blockingWorkloadIdentityReferenceLimit = 5
	openshiftAuthenticationName            = "cluster"
)

// OpenShiftServiceAccountIssuerClient reads and writes the OpenShift service account issuer.
type OpenShiftServiceAccountIssuerClient interface {
	Get(ctx context.Context) (string, error)
	Set(ctx context.Context, issuerURL string) (bool, error)
	WaitForKubeAPIServerRollout(ctx context.Context, changedAfter time.Time) error
}

// ServiceAccountTokenClient reads the issuer from tokens minted by the cluster.
type ServiceAccountTokenClient interface {
	CurrentIssuer(ctx context.Context) (string, error)
}

// OIDCIssuerReconciler reconciles an OIDCIssuer object.
type OIDCIssuerReconciler struct {
	client.Client
	Scheme                        *runtime.Scheme
	Publisher                     oidc.Publisher
	OpenShiftServiceAccountIssuer OpenShiftServiceAccountIssuerClient
	ServiceAccountTokens          ServiceAccountTokenClient
	SigningKeyRefreshInterval     time.Duration
}

// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=oidcissuers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=oidcissuers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=oidcissuers/finalizers,verbs=update
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=workloadidentities,verbs=get;list;watch
// +kubebuilder:rbac:groups=config.openshift.io,resources=authentications,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators,verbs=get

func (r *OIDCIssuerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	issuer := &azworkloadidentityv1alpha1.OIDCIssuer{}
	if err := r.Get(ctx, req.NamespacedName, issuer); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if issuer.Name != azworkloadidentityv1alpha1.OIDCIssuerName {
		log.Info("Ignoring OIDCIssuer with unsupported name", "expectedName", azworkloadidentityv1alpha1.OIDCIssuerName)
		return ctrl.Result{}, r.setNotReady(ctx, issuer, "InvalidName", fmt.Sprintf("OIDCIssuer must be named %q", azworkloadidentityv1alpha1.OIDCIssuerName))
	}

	if !issuer.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, issuer)
	}

	if !controllerutil.ContainsFinalizer(issuer, oidcIssuerFinalizer) {
		controllerutil.AddFinalizer(issuer, oidcIssuerFinalizer)
		if err := r.Update(ctx, issuer); err != nil {
			return ctrl.Result{}, err
		}
	}

	if r.Publisher == nil {
		return ctrl.Result{}, r.setNotReady(ctx, issuer, "PublisherNotConfigured", "OIDC document publisher is not configured")
	}

	published, err := r.Publisher.Publish(ctx, issuer)
	if err != nil {
		log.Error(err, "Failed to publish OIDC documents")
		statusErr := r.setNotReady(ctx, issuer, "PublishFailed", err.Error())
		if statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, err
	}

	if err := r.setPublished(ctx, issuer, published); err != nil {
		return ctrl.Result{}, err
	}

	if shouldUpdateOpenShiftServiceAccountIssuer(issuer) {
		if err := r.reconcileOpenShiftServiceAccountIssuer(ctx, issuer, published.IssuerURL); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.setReady(ctx, issuer, published); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: r.signingKeyRefreshInterval()}, nil
}

func (r *OIDCIssuerReconciler) signingKeyRefreshInterval() time.Duration {
	if r.SigningKeyRefreshInterval > 0 {
		return r.SigningKeyRefreshInterval
	}
	return defaultSigningKeyRefreshInterval
}

func (r *OIDCIssuerReconciler) reconcileDelete(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(issuer, oidcIssuerFinalizer) {
		return ctrl.Result{}, nil
	}

	blocked, err := r.blockDeleteIfWorkloadIdentitiesExist(ctx, issuer)
	if err != nil {
		return ctrl.Result{}, err
	}
	if blocked {
		return ctrl.Result{}, nil
	}

	blocked, err = r.blockDeleteIfClusterStillMintsIssuer(ctx, issuer)
	if err != nil {
		return ctrl.Result{}, err
	}
	if blocked {
		return ctrl.Result{RequeueAfter: defaultServiceAccountIssuerCheckPeriod}, nil
	}

	blocked, err = r.blockDeleteIfOpenShiftStillUsesIssuer(ctx, issuer)
	if err != nil {
		return ctrl.Result{}, err
	}
	if blocked {
		return ctrl.Result{}, nil
	}

	if issuer.Spec.DeletionPolicy == azworkloadidentityv1alpha1.DeletionPolicyDelete {
		if r.Publisher == nil {
			return ctrl.Result{}, fmt.Errorf("OIDC document publisher is not configured")
		}
		if err := r.Publisher.Delete(ctx, issuer); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to delete OIDC publisher resources")
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(issuer, oidcIssuerFinalizer)
	return ctrl.Result{}, r.Update(ctx, issuer)
}

func (r *OIDCIssuerReconciler) blockDeleteIfWorkloadIdentitiesExist(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) (bool, error) {
	identities := &azworkloadidentityv1alpha1.WorkloadIdentityList{}
	if err := r.List(ctx, identities); err != nil {
		return false, fmt.Errorf("list WorkloadIdentities before OIDCIssuer deletion: %w", err)
	}
	if len(identities.Items) == 0 {
		return false, nil
	}

	message := workloadidentity.DeletionBlockedMessage(identities.Items, blockingWorkloadIdentityReferenceLimit)
	logf.FromContext(ctx).Info("Blocked OIDCIssuer deletion because WorkloadIdentities still exist", "count", len(identities.Items))
	return true, r.setNotReady(ctx, issuer, "BlockedByWorkloadIdentities", message)
}

func (r *OIDCIssuerReconciler) blockDeleteIfClusterStillMintsIssuer(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) (bool, error) {
	if !oidcissuer.HasPublishedIssuerURL(issuer) {
		return false, nil
	}
	if r.ServiceAccountTokens == nil {
		if r.OpenShiftServiceAccountIssuer != nil {
			return false, nil
		}
		message := oidcissuer.ClusterServiceAccountIssuerGuardUnavailableMessage(issuer.Status.IssuerURL)
		logf.FromContext(ctx).Info("Blocked OIDCIssuer deletion because no cluster service account issuer guard is configured", "issuerURL", issuer.Status.IssuerURL)
		return true, r.setNotReady(ctx, issuer, "ClusterServiceAccountIssuerGuardUnavailable", message)
	}

	currentIssuer, err := r.ServiceAccountTokens.CurrentIssuer(ctx)
	if err != nil {
		message := oidcissuer.ClusterServiceAccountIssuerCheckFailedMessage(issuer.Status.IssuerURL, err)
		statusErr := r.setNotReady(ctx, issuer, "ClusterServiceAccountIssuerCheckFailed", message)
		if statusErr != nil {
			return false, statusErr
		}
		return false, fmt.Errorf("verify cluster service account token issuer before OIDCIssuer deletion: %w", err)
	}
	if currentIssuer != issuer.Status.IssuerURL {
		return false, nil
	}

	message := oidcissuer.ClusterServiceAccountIssuerDeletionBlockedMessage(issuer.Status.IssuerURL)
	logf.FromContext(ctx).Info("Blocked OIDCIssuer deletion because the cluster still mints service account tokens with its issuer URL", "issuerURL", issuer.Status.IssuerURL)
	return true, r.setNotReady(ctx, issuer, "BlockedByClusterServiceAccountIssuer", message)
}

func (r *OIDCIssuerReconciler) blockDeleteIfOpenShiftStillUsesIssuer(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) (bool, error) {
	if !oidcissuer.HasPublishedIssuerURL(issuer) || r.OpenShiftServiceAccountIssuer == nil {
		return false, nil
	}

	currentIssuer, err := r.OpenShiftServiceAccountIssuer.Get(ctx)
	if err != nil {
		return false, fmt.Errorf("read OpenShift service account issuer before OIDCIssuer deletion: %w", err)
	}
	if currentIssuer != issuer.Status.IssuerURL {
		return false, nil
	}

	message := oidcissuer.OpenShiftServiceAccountIssuerDeletionBlockedMessage(issuer.Status.IssuerURL)
	logf.FromContext(ctx).Info("Blocked OIDCIssuer deletion because OpenShift still uses its issuer URL", "issuerURL", issuer.Status.IssuerURL)
	return true, r.setNotReady(ctx, issuer, "BlockedByOpenShiftServiceAccountIssuer", message)
}

func (r *OIDCIssuerReconciler) reconcileOpenShiftServiceAccountIssuer(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer, issuerURL string) error {
	log := logf.FromContext(ctx)

	if r.OpenShiftServiceAccountIssuer == nil {
		return r.setNotReady(ctx, issuer, "OpenShiftIssuerClientNotConfigured", "OpenShift service account issuer client is not configured")
	}

	if issuer.Status.PreviousServiceAccountIssuer == nil {
		currentIssuer, err := r.OpenShiftServiceAccountIssuer.Get(ctx)
		if err != nil {
			log.Error(err, "Failed to read OpenShift service account issuer")
			statusErr := r.setNotReady(ctx, issuer, "OpenShiftIssuerReadFailed", err.Error())
			if statusErr != nil {
				return statusErr
			}
			return err
		}
		if currentIssuer != issuerURL {
			if err := r.capturePreviousServiceAccountIssuer(ctx, issuer, currentIssuer); err != nil {
				return err
			}
		}
	}

	changedAt := time.Now()
	changed, err := r.OpenShiftServiceAccountIssuer.Set(ctx, issuerURL)
	if err != nil {
		log.Error(err, "Failed to set OpenShift service account issuer")
		statusErr := r.setNotReady(ctx, issuer, "OpenShiftIssuerUpdateFailed", err.Error())
		if statusErr != nil {
			return statusErr
		}
		return err
	}
	if changed {
		if err := r.OpenShiftServiceAccountIssuer.WaitForKubeAPIServerRollout(ctx, changedAt); err != nil {
			statusErr := r.setNotReady(ctx, issuer, "OpenShiftIssuerRolloutFailed", err.Error())
			if statusErr != nil {
				return statusErr
			}
			return fmt.Errorf("wait for OpenShift rollout after service account issuer update: %w", err)
		}
	}
	return nil
}

func (r *OIDCIssuerReconciler) capturePreviousServiceAccountIssuer(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer, previousIssuer string) error {
	original := issuer.DeepCopy()
	issuer.Status.PreviousServiceAccountIssuer = &previousIssuer
	return r.Status().Patch(ctx, issuer, client.MergeFrom(original))
}

func shouldUpdateOpenShiftServiceAccountIssuer(issuer *azworkloadidentityv1alpha1.OIDCIssuer) bool {
	return issuer.Spec.OpenShift != nil && issuer.Spec.OpenShift.UpdateServiceAccountIssuer
}

func (r *OIDCIssuerReconciler) setPublished(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer, published oidc.PublishedDocuments) error {
	original := issuer.DeepCopy()
	now := metav1.Now()
	issuer.Status.IssuerURL = published.IssuerURL
	issuer.Status.AzureResources = published.AzureResources
	issuer.Status.ObservedGeneration = issuer.Generation
	issuer.Status.LastReconciledTime = &now
	return r.Status().Patch(ctx, issuer, client.MergeFrom(original))
}

func (r *OIDCIssuerReconciler) setReady(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer, published oidc.PublishedDocuments) error {
	original := issuer.DeepCopy()
	now := metav1.Now()
	issuer.Status.IssuerURL = published.IssuerURL
	issuer.Status.AzureResources = published.AzureResources
	issuer.Status.ObservedGeneration = issuer.Generation
	issuer.Status.LastReconciledTime = &now
	apimeta.SetStatusCondition(&issuer.Status.Conditions, metav1.Condition{
		Type:               string(azworkloadidentityv1alpha1.OIDCIssuerConditionReady),
		Status:             metav1.ConditionTrue,
		Reason:             "Published",
		Message:            "OIDC issuer documents are published",
		ObservedGeneration: issuer.Generation,
	})
	return r.Status().Patch(ctx, issuer, client.MergeFrom(original))
}

func (r *OIDCIssuerReconciler) setNotReady(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer, reason, message string) error {
	original := issuer.DeepCopy()
	now := metav1.Now()
	issuer.Status.ObservedGeneration = issuer.Generation
	issuer.Status.LastReconciledTime = &now
	apimeta.SetStatusCondition(&issuer.Status.Conditions, metav1.Condition{
		Type:               string(azworkloadidentityv1alpha1.OIDCIssuerConditionReady),
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: issuer.Generation,
	})
	return r.Status().Patch(ctx, issuer, client.MergeFrom(original))
}

func (r *OIDCIssuerReconciler) oidcIssuerForWorkloadIdentity(context.Context, client.Object) []reconcile.Request {
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: azworkloadidentityv1alpha1.OIDCIssuerName},
	}}
}

func (r *OIDCIssuerReconciler) oidcIssuerForAuthentication(_ context.Context, obj client.Object) []reconcile.Request {
	if obj.GetName() != openshiftAuthenticationName {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Name: azworkloadidentityv1alpha1.OIDCIssuerName},
	}}
}

func (r *OIDCIssuerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	builder := ctrl.NewControllerManagedBy(mgr).
		For(&azworkloadidentityv1alpha1.OIDCIssuer{}).
		Watches(&azworkloadidentityv1alpha1.WorkloadIdentity{}, handler.EnqueueRequestsFromMapFunc(r.oidcIssuerForWorkloadIdentity))
	if r.OpenShiftServiceAccountIssuer != nil {
		builder = builder.Watches(&configv1.Authentication{}, handler.EnqueueRequestsFromMapFunc(r.oidcIssuerForAuthentication))
	}
	return builder.Named("oidcissuer").Complete(r)
}
