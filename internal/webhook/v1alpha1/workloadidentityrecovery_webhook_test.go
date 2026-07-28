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
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

const (
	recoveryTestCurrentUID   types.UID = "11111111-1111-1111-1111-111111111111"
	recoveryTestPreviousUID  types.UID = "22222222-2222-2222-2222-222222222222"
	recoveryTestIdentityName           = "example"
)

func TestWorkloadIdentityRecoveryValidatorAcceptsExactRecoveryRequiredTarget(t *testing.T) {
	validator, recovery := recoveryValidatorForTest(t)

	if _, err := validator.ValidateCreate(context.Background(), recovery); err != nil {
		t.Fatalf("validate recovery: %v", err)
	}
}

func TestWorkloadIdentityRecoveryValidatorRejectsMismatchedEvidence(t *testing.T) {
	validator, recovery := recoveryValidatorForTest(t)
	recovery.Spec.PreviousWorkloadIdentityUID = "different-source"

	if _, err := validator.ValidateCreate(context.Background(), recovery); err == nil {
		t.Fatal("expected mismatched recovery evidence to be rejected")
	}
}

func TestWorkloadIdentityRecoveryValidatorRejectsStaleTargetUID(t *testing.T) {
	validator, recovery := recoveryValidatorForTest(t)
	recovery.Spec.WorkloadIdentityRef.UID = "stale-current"

	if _, err := validator.ValidateCreate(context.Background(), recovery); err == nil {
		t.Fatal("expected stale target UID to be rejected")
	}
}

func TestWorkloadIdentityRecoveryValidatorRejectsCurrentUIDAsSource(t *testing.T) {
	validator, recovery := recoveryValidatorForTest(t)
	recovery.Spec.PreviousWorkloadIdentityUID = recovery.Spec.WorkloadIdentityRef.UID

	if _, err := validator.ValidateCreate(context.Background(), recovery); err == nil {
		t.Fatal("expected the current UID as recovery source to be rejected")
	}
}

func TestWorkloadIdentityRecoveryValidatorRejectsDuplicateSourceUID(t *testing.T) {
	scheme := recoveryWebhookTestScheme(t)
	identity := recoveryRequiredIdentity()
	existing := validRecovery()
	existing.Name = "existing-recovery"
	kubeClient := recoveryWebhookTestClient(scheme, identity, existing)
	validator := &WorkloadIdentityRecoveryValidator{
		Client:         kubeClient,
		RecoveryClient: kubeClient,
	}
	candidate := validRecovery()
	candidate.Name = "candidate-recovery"

	_, err := validator.ValidateCreate(context.Background(), candidate)
	if !apierrors.IsAlreadyExists(err) {
		t.Fatalf("duplicate recovery error = %v, want AlreadyExists", err)
	}
}

func TestWorkloadIdentityRecoveryValidatorAllowsCreateWhenAdvisoryDuplicateLookupFails(t *testing.T) {
	scheme := recoveryWebhookTestScheme(t)
	identity := recoveryRequiredIdentity()
	kubeClient := recoveryWebhookTestClient(scheme, identity)
	validator := &WorkloadIdentityRecoveryValidator{
		Client: kubeClient,
		RecoveryClient: &failingRecoveryListReader{
			Reader: kubeClient,
			err:    errors.New("temporary recovery cache failure"),
		},
	}

	if _, err := validator.ValidateCreate(context.Background(), validRecovery()); err != nil {
		t.Fatalf("advisory duplicate lookup failure rejected recovery: %v", err)
	}
}

func TestWorkloadIdentityRecoveryValidatorKeepsSpecImmutableAndAllowsDeletion(t *testing.T) {
	validator, recovery := recoveryValidatorForTest(t)
	updated := recovery.DeepCopy()
	updated.Status.CommitVerified = true
	if _, err := validator.ValidateUpdate(context.Background(), recovery, updated); err != nil {
		t.Fatalf("status-only update rejected: %v", err)
	}

	updated.Spec.PreviousWorkloadIdentityUID = "different-source"
	if _, err := validator.ValidateUpdate(context.Background(), recovery, updated); err == nil {
		t.Fatal("expected spec update to be rejected")
	}
	if _, err := validator.ValidateDelete(context.Background(), recovery); err != nil {
		t.Fatalf("delete rejected: %v", err)
	}
}

func recoveryValidatorForTest(
	t *testing.T,
) (*WorkloadIdentityRecoveryValidator, *workloadidentityv1alpha1.WorkloadIdentityRecovery) {
	t.Helper()
	scheme := recoveryWebhookTestScheme(t)
	identity := recoveryRequiredIdentity()
	kubeClient := recoveryWebhookTestClient(scheme, identity)
	return &WorkloadIdentityRecoveryValidator{
		Client:         kubeClient,
		RecoveryClient: kubeClient,
	}, validRecovery()
}

func recoveryWebhookTestClient(
	scheme *runtime.Scheme,
	objects ...client.Object,
) client.Client {
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithIndex(
			&workloadidentityv1alpha1.WorkloadIdentityRecovery{},
			workloadidentity.RecoveryPreviousWorkloadIdentityUIDIndex,
			func(object client.Object) []string {
				recovery := object.(*workloadidentityv1alpha1.WorkloadIdentityRecovery)
				return []string{string(recovery.Spec.PreviousWorkloadIdentityUID)}
			},
		).
		Build()
}

func recoveryWebhookTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := workloadidentityv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add API to scheme: %v", err)
	}
	return scheme
}

func recoveryRequiredIdentity() *workloadidentityv1alpha1.WorkloadIdentity {
	return &workloadidentityv1alpha1.WorkloadIdentity{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  defaultWebhookNamespace,
			Name:       recoveryTestIdentityName,
			UID:        recoveryTestCurrentUID,
			Generation: 3,
		},
		Status: workloadidentityv1alpha1.WorkloadIdentityStatus{
			ObservedGeneration: 3,
			Recovery: &workloadidentityv1alpha1.WorkloadIdentityRecoveryRequiredStatus{
				PreviousWorkloadIdentityUID: recoveryTestPreviousUID,
			},
			Conditions: []metav1.Condition{{
				Type:               string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
				Status:             metav1.ConditionFalse,
				Reason:             workloadidentity.ReasonRecoveryRequired,
				ObservedGeneration: 3,
			}},
		},
	}
}

func validRecovery() *workloadidentityv1alpha1.WorkloadIdentityRecovery {
	return &workloadidentityv1alpha1.WorkloadIdentityRecovery{
		ObjectMeta: metav1.ObjectMeta{Name: "recover-example"},
		Spec: workloadidentityv1alpha1.WorkloadIdentityRecoverySpec{
			WorkloadIdentityRef: workloadidentityv1alpha1.WorkloadIdentityRecoveryReference{
				Namespace: defaultWebhookNamespace,
				Name:      recoveryTestIdentityName,
				UID:       recoveryTestCurrentUID,
			},
			PreviousWorkloadIdentityUID: recoveryTestPreviousUID,
		},
	}
}

type failingRecoveryListReader struct {
	client.Reader
	err error
}

func (r *failingRecoveryListReader) List(
	context.Context,
	client.ObjectList,
	...client.ListOption,
) error {
	return r.err
}
