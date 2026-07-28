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
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
)

func primaryResourcePredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: primaryResourceUpdate,
	}
}

func workloadIdentityPrimaryPredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			if primaryResourceUpdate(updateEvent) {
				return true
			}
			oldIdentity, oldOK := updateEvent.ObjectOld.(*azworkloadidentityv1alpha1.WorkloadIdentity)
			newIdentity, newOK := updateEvent.ObjectNew.(*azworkloadidentityv1alpha1.WorkloadIdentity)
			if !oldOK || !newOK {
				return false
			}
			return workloadIdentityRecoveryWakeTransition(oldIdentity, newIdentity)
		},
	}
}

func workloadIdentityRecoveryTargetPredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			if primaryResourceUpdate(updateEvent) {
				return true
			}
			oldIdentity, oldOK := updateEvent.ObjectOld.(*azworkloadidentityv1alpha1.WorkloadIdentity)
			newIdentity, newOK := updateEvent.ObjectNew.(*azworkloadidentityv1alpha1.WorkloadIdentity)
			if !oldOK || !newOK {
				return false
			}
			return workloadIdentityRecoveryStateChanged(oldIdentity, newIdentity)
		},
	}
}

func workloadIdentityRecoveryStateChanged(
	oldIdentity, newIdentity *azworkloadidentityv1alpha1.WorkloadIdentity,
) bool {
	oldPreviousUID := ""
	if oldIdentity.Status.Recovery != nil {
		oldPreviousUID = string(oldIdentity.Status.Recovery.PreviousWorkloadIdentityUID)
	}
	newPreviousUID := ""
	if newIdentity.Status.Recovery != nil {
		newPreviousUID = string(newIdentity.Status.Recovery.PreviousWorkloadIdentityUID)
	}
	if oldPreviousUID != newPreviousUID {
		return true
	}
	if oldIdentity.Status.Recovery == nil && newIdentity.Status.Recovery == nil {
		return false
	}

	oldReady := apimeta.FindStatusCondition(
		oldIdentity.Status.Conditions,
		string(azworkloadidentityv1alpha1.WorkloadIdentityConditionReady),
	)
	newReady := apimeta.FindStatusCondition(
		newIdentity.Status.Conditions,
		string(azworkloadidentityv1alpha1.WorkloadIdentityConditionReady),
	)
	if oldReady == nil || newReady == nil {
		return oldReady != newReady
	}
	return oldReady.Status != newReady.Status ||
		oldReady.Reason != newReady.Reason ||
		oldReady.ObservedGeneration != newReady.ObservedGeneration
}

func workloadIdentityRecoveryWakeTransition(
	oldIdentity, newIdentity *azworkloadidentityv1alpha1.WorkloadIdentity,
) bool {
	if !workloadIdentityRecoveryIsInProgress(oldIdentity) &&
		workloadIdentityRecoveryIsInProgress(newIdentity) {
		return true
	}

	oldReady := apimeta.FindStatusCondition(
		oldIdentity.Status.Conditions,
		string(azworkloadidentityv1alpha1.WorkloadIdentityConditionReady),
	)
	newReady := apimeta.FindStatusCondition(
		newIdentity.Status.Conditions,
		string(azworkloadidentityv1alpha1.WorkloadIdentityConditionReady),
	)
	if newReady == nil || newReady.Status != metav1.ConditionFalse {
		return false
	}
	if oldReady != nil &&
		oldReady.Status == newReady.Status &&
		oldReady.Reason == newReady.Reason {
		return false
	}
	return newReady.Reason == recoveryReasonCompleted ||
		newReady.Reason == recoveryReasonCancelled
}

func primaryResourceUpdate(updateEvent event.UpdateEvent) bool {
	if updateEvent.ObjectOld == nil || updateEvent.ObjectNew == nil {
		return false
	}
	if updateEvent.ObjectOld.GetGeneration() != updateEvent.ObjectNew.GetGeneration() {
		return true
	}
	return updateEvent.ObjectOld.GetDeletionTimestamp() == nil &&
		updateEvent.ObjectNew.GetDeletionTimestamp() != nil
}

func createDeleteOnlyPredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

func oidcIssuerDependencyPredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			oldIssuer, oldOK := updateEvent.ObjectOld.(*azworkloadidentityv1alpha1.OIDCIssuer)
			newIssuer, newOK := updateEvent.ObjectNew.(*azworkloadidentityv1alpha1.OIDCIssuer)
			if !oldOK || !newOK {
				return false
			}
			if oldIssuer.Generation != newIssuer.Generation {
				return true
			}
			if oldIssuer.DeletionTimestamp == nil && newIssuer.DeletionTimestamp != nil {
				return true
			}
			return oldIssuer.Status.IssuerURL != newIssuer.Status.IssuerURL || isOIDCIssuerReady(oldIssuer) != isOIDCIssuerReady(newIssuer)
		},
	}
}

func serviceAccountDependencyPredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			oldServiceAccount, oldOK := updateEvent.ObjectOld.(*corev1.ServiceAccount)
			newServiceAccount, newOK := updateEvent.ObjectNew.(*corev1.ServiceAccount)
			if !oldOK || !newOK {
				return false
			}
			if oldServiceAccount.DeletionTimestamp == nil && newServiceAccount.DeletionTimestamp != nil {
				return true
			}
			for _, key := range []string{serviceAccountUseLabel, serviceAccountManagedBy, serviceAccountUID, serviceAccountCreatedBy} {
				if oldServiceAccount.Labels[key] != newServiceAccount.Labels[key] {
					return true
				}
			}
			for _, key := range []string{serviceAccountClientID, serviceAccountTenantID} {
				if oldServiceAccount.Annotations[key] != newServiceAccount.Annotations[key] {
					return true
				}
			}
			return false
		},
	}
}
