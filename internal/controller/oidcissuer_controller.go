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

	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/az-workload-identity-operator/internal/oidc"
)

const oidcIssuerFinalizer = "workloadidentity.azure.micosolutions.se/oidcissuer-finalizer"

const defaultSigningKeyRefreshInterval = 5 * time.Minute

type ServiceAccountIssuerUpdater interface {
	UpdateServiceAccountIssuer(ctx context.Context, issuerURL string) error
}

// OIDCIssuerReconciler reconciles an OIDCIssuer object.
type OIDCIssuerReconciler struct {
	client.Client
	Scheme                      *runtime.Scheme
	Publisher                   oidc.Publisher
	ServiceAccountIssuerUpdater ServiceAccountIssuerUpdater
	SigningKeyRefreshInterval   time.Duration
}

// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=oidcissuers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=oidcissuers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloadidentity.azure.micosolutions.se,resources=oidcissuers/finalizers,verbs=update
// +kubebuilder:rbac:groups=config.openshift.io,resources=authentications,verbs=get;list;watch;update;patch

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
		return ctrl.Result{}, r.reconcileDelete(ctx, issuer)
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

	if issuer.Spec.OpenShift != nil && issuer.Spec.OpenShift.UpdateServiceAccountIssuer {
		if r.ServiceAccountIssuerUpdater == nil {
			return ctrl.Result{}, r.setNotReady(ctx, issuer, "OpenShiftUpdaterNotConfigured", "OpenShift service account issuer updater is not configured")
		}
		if err := r.ServiceAccountIssuerUpdater.UpdateServiceAccountIssuer(ctx, published.IssuerURL); err != nil {
			log.Error(err, "Failed to update OpenShift service account issuer")
			statusErr := r.setNotReady(ctx, issuer, "OpenShiftIssuerUpdateFailed", err.Error())
			if statusErr != nil {
				return ctrl.Result{}, statusErr
			}
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

func (r *OIDCIssuerReconciler) reconcileDelete(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) error {
	if !controllerutil.ContainsFinalizer(issuer, oidcIssuerFinalizer) {
		return nil
	}

	if issuer.Spec.DeletionPolicy == azworkloadidentityv1alpha1.DeletionPolicyDelete {
		if r.Publisher == nil {
			return fmt.Errorf("OIDC document publisher is not configured")
		}
		if err := r.Publisher.Delete(ctx, issuer); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to delete OIDC publisher resources")
			return err
		}
	}

	controllerutil.RemoveFinalizer(issuer, oidcIssuerFinalizer)
	return r.Update(ctx, issuer)
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

func (r *OIDCIssuerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&azworkloadidentityv1alpha1.OIDCIssuer{}).
		Named("oidcissuer").
		Complete(r)
}
