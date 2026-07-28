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
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

var workloadidentityrecoverylog = logf.Log.WithName("workloadidentityrecovery-resource")

// SetupWorkloadIdentityRecoveryWebhookWithManager registers the webhook for WorkloadIdentityRecovery in the manager.
func SetupWorkloadIdentityRecoveryWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &workloadidentityv1alpha1.WorkloadIdentityRecovery{}).
		WithValidator(&WorkloadIdentityRecoveryValidator{
			Client:         mgr.GetAPIReader(),
			RecoveryClient: mgr.GetClient(),
		}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-workloadidentity-azure-micosolutions-se-v1alpha1-workloadidentityrecovery,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloadidentity.azure.micosolutions.se,resources=workloadidentityrecoveries,verbs=create;update;delete,versions=v1alpha1,name=vworkloadidentityrecovery-v1alpha1.workloadidentity.azure.micosolutions.se,admissionReviewVersions=v1

// WorkloadIdentityRecoveryValidator validates WorkloadIdentityRecovery admission requests.
type WorkloadIdentityRecoveryValidator struct {
	Client         client.Reader
	RecoveryClient client.Reader
}

func (v *WorkloadIdentityRecoveryValidator) ValidateCreate(
	ctx context.Context,
	obj *workloadidentityv1alpha1.WorkloadIdentityRecovery,
) (admission.Warnings, error) {
	workloadidentityrecoverylog.Info(
		"Validating WorkloadIdentityRecovery creation",
		"name",
		obj.Name,
	)
	if v.Client == nil {
		return nil, errors.New("WorkloadIdentityRecovery validator client is not configured")
	}

	specPath := field.NewPath("spec")
	var allErrs field.ErrorList
	if obj.Spec.PreviousWorkloadIdentityUID == obj.Spec.WorkloadIdentityRef.UID {
		allErrs = append(allErrs, field.Invalid(
			specPath.Child("previousWorkloadIdentityUid"),
			obj.Spec.PreviousWorkloadIdentityUID,
			"must differ from workloadIdentityRef.uid",
		))
	}
	identity := &workloadidentityv1alpha1.WorkloadIdentity{}
	key := types.NamespacedName{
		Namespace: obj.Spec.WorkloadIdentityRef.Namespace,
		Name:      obj.Spec.WorkloadIdentityRef.Name,
	}
	if err := v.Client.Get(ctx, key, identity); err != nil {
		if apierrors.IsNotFound(err) {
			allErrs = append(allErrs, field.NotFound(specPath.Child("workloadIdentityRef"), key.String()))
		} else {
			return nil, err
		}
	} else {
		allErrs = append(allErrs, validateRecoveryTarget(obj, identity, specPath)...)
	}

	recoveryClient := v.RecoveryClient
	if recoveryClient == nil {
		recoveryClient = v.Client
	}
	recoveries := &workloadidentityv1alpha1.WorkloadIdentityRecoveryList{}
	if err := recoveryClient.List(
		ctx,
		recoveries,
		client.MatchingFields{
			workloadidentity.RecoveryPreviousWorkloadIdentityUIDIndex: string(obj.Spec.PreviousWorkloadIdentityUID),
		},
	); err != nil {
		workloadidentityrecoverylog.Error(
			err,
			"Could not check for duplicate WorkloadIdentityRecovery",
			"previousWorkloadIdentityUid",
			obj.Spec.PreviousWorkloadIdentityUID,
		)
	} else {
		for i := range recoveries.Items {
			existing := &recoveries.Items[i]
			if existing.Spec.PreviousWorkloadIdentityUID == obj.Spec.PreviousWorkloadIdentityUID {
				return nil, apierrors.NewAlreadyExists(schema.GroupResource{
					Group:    workloadidentityv1alpha1.GroupVersion.Group,
					Resource: "workloadidentityrecoveries",
				}, existing.Name)
			}
		}
	}
	if len(allErrs) > 0 {
		return nil, invalidWorkloadIdentityRecovery(obj, allErrs)
	}
	return nil, nil
}

func (v *WorkloadIdentityRecoveryValidator) ValidateUpdate(
	_ context.Context,
	oldObj, newObj *workloadidentityv1alpha1.WorkloadIdentityRecovery,
) (admission.Warnings, error) {
	workloadidentityrecoverylog.Info(
		"Validating WorkloadIdentityRecovery update",
		"name",
		newObj.Name,
	)
	if reflect.DeepEqual(oldObj.Spec, newObj.Spec) {
		return nil, nil
	}
	return nil, invalidWorkloadIdentityRecovery(newObj, field.ErrorList{
		field.Forbidden(field.NewPath("spec"), "field is immutable"),
	})
}

func (v *WorkloadIdentityRecoveryValidator) ValidateDelete(
	_ context.Context,
	obj *workloadidentityv1alpha1.WorkloadIdentityRecovery,
) (admission.Warnings, error) {
	workloadidentityrecoverylog.Info(
		"Validating WorkloadIdentityRecovery deletion",
		"name",
		obj.Name,
	)
	return nil, nil
}

func validateRecoveryTarget(
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	specPath *field.Path,
) field.ErrorList {
	var allErrs field.ErrorList
	refPath := specPath.Child("workloadIdentityRef")
	if identity.UID != recovery.Spec.WorkloadIdentityRef.UID {
		allErrs = append(allErrs, field.Invalid(
			refPath.Child("uid"),
			recovery.Spec.WorkloadIdentityRef.UID,
			fmt.Sprintf("current WorkloadIdentity UID is %q", identity.UID),
		))
	}
	if !identity.DeletionTimestamp.IsZero() {
		allErrs = append(allErrs, field.Forbidden(refPath, "target WorkloadIdentity is being deleted"))
	}
	ready := apimeta.FindStatusCondition(
		identity.Status.Conditions,
		string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
	)
	if identity.Status.ObservedGeneration != identity.Generation ||
		ready == nil ||
		ready.ObservedGeneration != identity.Generation ||
		ready.Status != metav1.ConditionFalse ||
		ready.Reason != workloadidentity.ReasonRecoveryRequired {
		allErrs = append(allErrs, field.Forbidden(
			refPath,
			"target WorkloadIdentity is not in current-generation RecoveryRequired state",
		))
	}
	if identity.Status.Recovery == nil {
		allErrs = append(allErrs, field.Forbidden(
			specPath.Child("previousWorkloadIdentityUid"),
			"target WorkloadIdentity does not publish recovery evidence",
		))
	} else if identity.Status.Recovery.PreviousWorkloadIdentityUID != recovery.Spec.PreviousWorkloadIdentityUID {
		allErrs = append(allErrs, field.Invalid(
			specPath.Child("previousWorkloadIdentityUid"),
			recovery.Spec.PreviousWorkloadIdentityUID,
			fmt.Sprintf(
				"target WorkloadIdentity requires source UID %q",
				identity.Status.Recovery.PreviousWorkloadIdentityUID,
			),
		))
	}
	return allErrs
}

func invalidWorkloadIdentityRecovery(
	obj *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	errs field.ErrorList,
) error {
	return apierrors.NewInvalid(schema.GroupKind{
		Group: workloadidentityv1alpha1.GroupVersion.Group,
		Kind:  "WorkloadIdentityRecovery",
	}, obj.Name, errs)
}
