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
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var workloadidentitylog = logf.Log.WithName("workloadidentity-resource")

// SetupWorkloadIdentityWebhookWithManager registers the webhook for WorkloadIdentity in the manager.
func SetupWorkloadIdentityWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &workloadidentityv1alpha1.WorkloadIdentity{}).
		WithValidator(&WorkloadIdentityValidator{
			Client: mgr.GetAPIReader(),
		}).
		Complete()
}

// NOTE: If you want to customise the 'path', use the flags '--defaulting-path' or '--validation-path'.
// +kubebuilder:webhook:path=/validate-workloadidentity-azure-micosolutions-se-v1alpha1-workloadidentity,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloadidentity.azure.micosolutions.se,resources=workloadidentities,verbs=create;update,versions=v1alpha1,name=vworkloadidentity-v1alpha1.workloadidentity.azure.micosolutions.se,admissionReviewVersions=v1

// WorkloadIdentityValidator validates WorkloadIdentity admission requests.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type WorkloadIdentityValidator struct {
	Client client.Reader
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type WorkloadIdentity.
func (v *WorkloadIdentityValidator) ValidateCreate(ctx context.Context, obj *workloadidentityv1alpha1.WorkloadIdentity) (admission.Warnings, error) {
	workloadidentitylog.Info("Validation for WorkloadIdentity upon creation", "name", obj.GetName(), "namespace", obj.GetNamespace())
	return nil, v.validate(ctx, obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type WorkloadIdentity.
func (v *WorkloadIdentityValidator) ValidateUpdate(ctx context.Context, _ *workloadidentityv1alpha1.WorkloadIdentity, newObj *workloadidentityv1alpha1.WorkloadIdentity) (admission.Warnings, error) {
	workloadidentitylog.Info("Validation for WorkloadIdentity upon update", "name", newObj.GetName(), "namespace", newObj.GetNamespace())
	return nil, v.validate(ctx, newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type WorkloadIdentity.
func (v *WorkloadIdentityValidator) ValidateDelete(context.Context, *workloadidentityv1alpha1.WorkloadIdentity) (admission.Warnings, error) {
	return nil, nil
}

func (v *WorkloadIdentityValidator) validate(ctx context.Context, obj *workloadidentityv1alpha1.WorkloadIdentity) error {
	if v.Client == nil {
		return errors.New("WorkloadIdentity validator client is not configured")
	}

	identities := &workloadidentityv1alpha1.WorkloadIdentityList{}
	if err := v.Client.List(ctx, identities); err != nil {
		return err
	}

	var allErrs field.ErrorList
	for i := range identities.Items {
		existing := &identities.Items[i]
		if sameWorkloadIdentity(existing, obj) {
			continue
		}
		if existing.Namespace == obj.Namespace && existing.Spec.ServiceAccount.Name == obj.Spec.ServiceAccount.Name {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec", "serviceAccount", "name"),
				obj.Spec.ServiceAccount.Name,
				fmt.Sprintf("already referenced by WorkloadIdentity %s/%s", existing.Namespace, existing.Name),
			))
		}
		if sameFederatedCredential(existing.Spec.Azure, obj.Spec.Azure) {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec", "azure", "federatedIdentityCredentialName"),
				obj.Spec.Azure.FederatedIdentityCredentialName,
				fmt.Sprintf("Azure federated identity credential tuple already referenced by WorkloadIdentity %s/%s", existing.Namespace, existing.Name),
			))
		}
	}
	if len(allErrs) > 0 {
		return invalidWorkloadIdentity(obj, allErrs)
	}
	return nil
}

func sameWorkloadIdentity(a, b *workloadidentityv1alpha1.WorkloadIdentity) bool {
	return a.Namespace == b.Namespace && a.Name == b.Name
}

func sameFederatedCredential(a, b workloadidentityv1alpha1.AzureWorkloadIdentityConfig) bool {
	return strings.EqualFold(a.SubscriptionID, b.SubscriptionID) &&
		strings.EqualFold(a.ResourceGroupName, b.ResourceGroupName) &&
		strings.EqualFold(a.UserAssignedIdentityName, b.UserAssignedIdentityName) &&
		strings.EqualFold(a.FederatedIdentityCredentialName, b.FederatedIdentityCredentialName)
}

func invalidWorkloadIdentity(obj *workloadidentityv1alpha1.WorkloadIdentity, errs field.ErrorList) error {
	return apierrors.NewInvalid(schema.GroupKind{
		Group: workloadidentityv1alpha1.GroupVersion.Group,
		Kind:  "WorkloadIdentity",
	}, obj.GetName(), errs)
}
