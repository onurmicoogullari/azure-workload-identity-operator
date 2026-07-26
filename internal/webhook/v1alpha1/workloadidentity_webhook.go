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

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
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
func (v *WorkloadIdentityValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *workloadidentityv1alpha1.WorkloadIdentity) (admission.Warnings, error) {
	workloadidentitylog.Info("Validation for WorkloadIdentity upon update", "name", newObj.GetName(), "namespace", newObj.GetNamespace())
	allErrs, err := v.validationErrors(ctx, newObj)
	if err != nil {
		return nil, err
	}
	if oldObj.Spec.Azure.UserAssignedIdentityName != newObj.Spec.Azure.UserAssignedIdentityName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "azure", "userAssignedIdentityName"),
			"field is immutable",
		))
	}
	if oldObj.Spec.Azure.FederatedIdentityCredentialName != newObj.Spec.Azure.FederatedIdentityCredentialName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "azure", "federatedIdentityCredentialName"),
			"field is immutable",
		))
	}
	if oldObj.Spec.ServiceAccount.Name != newObj.Spec.ServiceAccount.Name {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "serviceAccount", "name"),
			"field is immutable",
		))
	}
	if len(allErrs) > 0 {
		return nil, invalidWorkloadIdentity(newObj, allErrs)
	}
	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type WorkloadIdentity.
func (v *WorkloadIdentityValidator) ValidateDelete(context.Context, *workloadidentityv1alpha1.WorkloadIdentity) (admission.Warnings, error) {
	return nil, nil
}

func (v *WorkloadIdentityValidator) validate(ctx context.Context, obj *workloadidentityv1alpha1.WorkloadIdentity) error {
	allErrs, err := v.validationErrors(ctx, obj)
	if err != nil {
		return err
	}
	if len(allErrs) > 0 {
		return invalidWorkloadIdentity(obj, allErrs)
	}
	return nil
}

func (v *WorkloadIdentityValidator) validationErrors(
	ctx context.Context,
	obj *workloadidentityv1alpha1.WorkloadIdentity,
) (field.ErrorList, error) {
	if v.Client == nil {
		return nil, errors.New("WorkloadIdentity validator client is not configured")
	}

	identities := &workloadidentityv1alpha1.WorkloadIdentityList{}
	if err := v.Client.List(ctx, identities); err != nil {
		return nil, err
	}

	var allErrs field.ErrorList
	resolvedName := workloadidentity.UserAssignedIdentityName(
		obj.Namespace,
		obj.Spec.Azure.UserAssignedIdentityName,
	)
	if err := workloadidentity.ValidateUserAssignedIdentityName(resolvedName); err != nil {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "azure", "userAssignedIdentityName"),
			obj.Spec.Azure.UserAssignedIdentityName,
			err.Error(),
		))
	}
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
		existingResolvedName := workloadidentity.UserAssignedIdentityName(
			existing.Namespace,
			existing.Spec.Azure.UserAssignedIdentityName,
		)
		if strings.EqualFold(existingResolvedName, resolvedName) {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("spec", "azure", "userAssignedIdentityName"),
				obj.Spec.Azure.UserAssignedIdentityName,
				fmt.Sprintf(
					"resolved Azure user assigned identity name %q is already referenced by WorkloadIdentity %s/%s",
					resolvedName,
					existing.Namespace,
					existing.Name,
				),
			))
		}
	}
	return allErrs, nil
}

func sameWorkloadIdentity(a, b *workloadidentityv1alpha1.WorkloadIdentity) bool {
	return a.Namespace == b.Namespace && a.Name == b.Name
}

func invalidWorkloadIdentity(obj *workloadidentityv1alpha1.WorkloadIdentity, errs field.ErrorList) error {
	return apierrors.NewInvalid(schema.GroupKind{
		Group: workloadidentityv1alpha1.GroupVersion.Group,
		Kind:  "WorkloadIdentity",
	}, obj.GetName(), errs)
}
