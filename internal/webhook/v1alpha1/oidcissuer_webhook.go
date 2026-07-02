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

package v1alpha1

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/az-workload-identity-operator/internal/oidcissuer"
)

const blockingWorkloadIdentityReferenceLimit = 5

// nolint:unused
// log is for logging in this package.
var oidcissuerlog = logf.Log.WithName("oidcissuer-resource")

// SetupOIDCIssuerWebhookWithManager registers the webhook for OIDCIssuer in the manager.
func SetupOIDCIssuerWebhookWithManager(mgr ctrl.Manager, openShiftServiceAccountIssuer openShiftServiceAccountIssuerGetter, serviceAccountTokens serviceAccountTokenClient) error {
	return ctrl.NewWebhookManagedBy(mgr, &workloadidentityv1alpha1.OIDCIssuer{}).
		WithValidator(&OIDCIssuerValidator{
			Client:                        mgr.GetAPIReader(),
			OpenShiftServiceAccountIssuer: openShiftServiceAccountIssuer,
			ServiceAccountTokens:          serviceAccountTokens,
		}).
		Complete()
}

// NOTE: If you want to customise the 'path', use the flags '--defaulting-path' or '--validation-path'.
// +kubebuilder:webhook:path=/validate-workloadidentity-azure-micosolutions-se-v1alpha1-oidcissuer,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloadidentity.azure.micosolutions.se,resources=oidcissuers,verbs=delete,versions=v1alpha1,name=voidcissuer-v1alpha1.workloadidentity.azure.micosolutions.se,admissionReviewVersions=v1

// OIDCIssuerValidator validates OIDCIssuer admission requests.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type OIDCIssuerValidator struct {
	Client                        client.Reader
	OpenShiftServiceAccountIssuer openShiftServiceAccountIssuerGetter
	ServiceAccountTokens          serviceAccountTokenClient
}

type openShiftServiceAccountIssuerGetter interface {
	Get(ctx context.Context) (string, error)
}

type serviceAccountTokenClient interface {
	CurrentIssuer(ctx context.Context) (string, error)
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type OIDCIssuer.
func (v *OIDCIssuerValidator) ValidateCreate(_ context.Context, obj *workloadidentityv1alpha1.OIDCIssuer) (admission.Warnings, error) {
	oidcissuerlog.Info("Validation for OIDCIssuer upon creation", "name", obj.GetName())
	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type OIDCIssuer.
func (v *OIDCIssuerValidator) ValidateUpdate(_ context.Context, oldObj, newObj *workloadidentityv1alpha1.OIDCIssuer) (admission.Warnings, error) {
	oidcissuerlog.Info("Validation for OIDCIssuer upon update", "name", newObj.GetName())
	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type OIDCIssuer.
func (v *OIDCIssuerValidator) ValidateDelete(ctx context.Context, obj *workloadidentityv1alpha1.OIDCIssuer) (admission.Warnings, error) {
	oidcissuerlog.Info("Validation for OIDCIssuer upon deletion", "name", obj.GetName())
	if obj.GetName() != workloadidentityv1alpha1.OIDCIssuerName {
		return nil, nil
	}
	if v.Client == nil {
		return nil, errors.New("OIDCIssuer validator client is not configured")
	}

	result, err := oidcissuer.CheckWorkloadIdentityDeletionBlock(ctx, v.Client, blockingWorkloadIdentityReferenceLimit)
	if err != nil {
		return nil, err
	}
	if result.Blocked {
		oidcissuerlog.Info("Rejected OIDCIssuer deletion because WorkloadIdentities still exist", "count", result.WorkloadIdentityCount)
		return nil, forbiddenOIDCIssuerDeletion(obj.GetName(), result.Message)
	}

	result, err = oidcissuer.CheckClusterServiceAccountIssuerHandoff(ctx, obj, v.ServiceAccountTokens, v.OpenShiftServiceAccountIssuer)
	if err != nil {
		return nil, err
	}
	if result.CheckFailed {
		return nil, forbiddenOIDCIssuerDeletion(obj.GetName(), result.Message)
	}
	if result.Blocked {
		if result.Reason == oidcissuer.ReasonBlockedByClusterServiceAccountIssuer {
			oidcissuerlog.Info("Rejected OIDCIssuer deletion because the cluster still mints service account tokens with its issuer URL", "issuerURL", obj.Status.IssuerURL)
		}
		return nil, forbiddenOIDCIssuerDeletion(obj.GetName(), result.Message)
	}

	result, err = oidcissuer.CheckOpenShiftServiceAccountIssuerHandoff(ctx, obj, v.OpenShiftServiceAccountIssuer)
	if err != nil {
		return nil, err
	}
	if result.Blocked {
		oidcissuerlog.Info("Rejected OIDCIssuer deletion because OpenShift still uses its issuer URL", "issuerURL", obj.Status.IssuerURL)
		return nil, forbiddenOIDCIssuerDeletion(obj.GetName(), result.Message)
	}
	return nil, nil
}

func forbiddenOIDCIssuerDeletion(name, message string) error {
	return apierrors.NewForbidden(schema.GroupResource{
		Group:    workloadidentityv1alpha1.GroupVersion.Group,
		Resource: "oidcissuers",
	}, name, errors.New(message))
}
