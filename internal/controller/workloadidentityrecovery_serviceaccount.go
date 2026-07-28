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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

func (r *WorkloadIdentityRecoveryReconciler) validateRecoveryServiceAccount(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
) error {
	serviceAccount := &corev1.ServiceAccount{}
	if err := r.recoveryReader().Get(ctx, serviceAccountKey(identity), serviceAccount); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return validateRecoverableServiceAccount(recovery, identity, serviceAccount)
}

func validateRecoverableServiceAccount(
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	serviceAccount *corev1.ServiceAccount,
) error {
	ownerUID := serviceAccount.Labels[serviceAccountUID]
	switch ownerUID {
	case string(recovery.Spec.PreviousWorkloadIdentityUID):
		if serviceAccount.Labels[serviceAccountManagedBy] != serviceAccountManagerName {
			return workloadidentity.NewRecoveryBlockedError(
				recoveryReasonServiceAccount,
				fmt.Sprintf(
					"ServiceAccount %q has source ownership without the operator marker",
					client.ObjectKeyFromObject(serviceAccount),
				),
			)
		}
	case "":
		if serviceAccount.Labels[serviceAccountManagedBy] != "" ||
			serviceAccount.Labels[serviceAccountCreatedBy] != "" {
			return workloadidentity.NewRecoveryBlockedError(
				recoveryReasonServiceAccount,
				fmt.Sprintf(
					"ServiceAccount %q has ambiguous operator ownership markers",
					client.ObjectKeyFromObject(serviceAccount),
				),
			)
		}
		if err := validateUnownedServiceAccountAnnotations(serviceAccount); err != nil {
			return workloadidentity.NewRecoveryBlockedError(recoveryReasonServiceAccount, err.Error())
		}
	case string(identity.UID):
		// A retry after transfer is safe; exact managed fields are verified below.
	default:
		return workloadidentity.NewRecoveryBlockedError(
			recoveryReasonServiceAccount,
			fmt.Sprintf(
				"ServiceAccount %q belongs to WorkloadIdentity UID %q",
				client.ObjectKeyFromObject(serviceAccount),
				ownerUID,
			),
		)
	}
	return nil
}

func (r *WorkloadIdentityRecoveryReconciler) transferRecoveryServiceAccount(
	ctx context.Context,
	recovery *workloadidentityv1alpha1.WorkloadIdentityRecovery,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
) (types.UID, error) {
	serviceAccount := &corev1.ServiceAccount{}
	reader := r.recoveryReader()
	if err := reader.Get(ctx, serviceAccountKey(identity), serviceAccount); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	if err := validateRecoverableServiceAccount(recovery, identity, serviceAccount); err != nil {
		return "", err
	}
	original := serviceAccount.DeepCopy()
	serviceAccount.Labels = maps.Clone(serviceAccount.Labels)
	if serviceAccount.Labels == nil {
		serviceAccount.Labels = map[string]string{}
	}
	serviceAccount.Annotations = maps.Clone(serviceAccount.Annotations)
	if serviceAccount.Annotations == nil {
		serviceAccount.Annotations = map[string]string{}
	}
	serviceAccount.Labels[serviceAccountUseLabel] = trueValue
	serviceAccount.Labels[serviceAccountManagedBy] = serviceAccountManagerName
	serviceAccount.Labels[serviceAccountUID] = string(identity.UID)
	serviceAccount.Annotations[serviceAccountClientID] = plan.UserAssignedIdentity.ClientID
	serviceAccount.Annotations[serviceAccountTenantID] = plan.UserAssignedIdentity.TenantID
	if !maps.Equal(original.Labels, serviceAccount.Labels) ||
		!maps.Equal(original.Annotations, serviceAccount.Annotations) {
		if err := r.Patch(
			ctx,
			serviceAccount,
			client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}),
		); err != nil {
			return "", err
		}
	}
	verified := &corev1.ServiceAccount{}
	if err := reader.Get(ctx, serviceAccountKey(identity), verified); err != nil {
		return "", err
	}
	if verified.UID != serviceAccount.UID ||
		!recoveryServiceAccountFieldsMatch(verified, identity, plan) {
		return "", workloadidentity.NewRecoveryBlockedError(
			recoveryReasonServiceAccount,
			"ServiceAccount recovery transfer could not be read-verified",
		)
	}
	return verified.UID, nil
}

func (r *WorkloadIdentityRecoveryReconciler) verifyRecoveryServiceAccountForCommit(
	ctx context.Context,
	reader client.Reader,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
	expectedUID types.UID,
) error {
	serviceAccount := &corev1.ServiceAccount{}
	if err := reader.Get(ctx, serviceAccountKey(identity), serviceAccount); err != nil {
		if apierrors.IsNotFound(err) && expectedUID == "" {
			return nil
		}
		return err
	}
	if expectedUID == "" {
		return workloadidentity.NewRecoveryBlockedError(
			recoveryReasonServiceAccount,
			"ServiceAccount appeared immediately before recovery commit; retrying transfer",
		)
	}
	if serviceAccount.UID != expectedUID ||
		!recoveryServiceAccountFieldsMatch(serviceAccount, identity, plan) {
		return workloadidentity.NewRecoveryBlockedError(
			recoveryReasonServiceAccount,
			"ServiceAccount changed after recovery transfer",
		)
	}
	return nil
}

func recoveryServiceAccountFieldsMatch(
	serviceAccount *corev1.ServiceAccount,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	plan *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
) bool {
	return serviceAccount.Labels[serviceAccountUseLabel] == trueValue &&
		serviceAccount.Labels[serviceAccountManagedBy] == serviceAccountManagerName &&
		serviceAccount.Labels[serviceAccountUID] == string(identity.UID) &&
		serviceAccount.Annotations[serviceAccountClientID] == plan.UserAssignedIdentity.ClientID &&
		serviceAccount.Annotations[serviceAccountTenantID] == plan.UserAssignedIdentity.TenantID
}
