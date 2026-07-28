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
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

const (
	recoveryControllerCurrentUID   types.UID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	recoveryControllerPreviousUID  types.UID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	recoveryControllerSAUID        types.UID = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	recoveryControllerIdentityName           = "example"
)

type fakeRecoveryManager struct {
	plan          *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan
	inspectErr    error
	markErr       error
	credentialErr error
	commitErr     error
	finalizeErr   error
	calls         []string
}

func (f *fakeRecoveryManager) Inspect(
	context.Context,
	*workloadidentityv1alpha1.WorkloadIdentityRecovery,
	*workloadidentityv1alpha1.WorkloadIdentity,
	string,
	string,
) (*workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan, error) {
	f.calls = append(f.calls, "inspect")
	if f.plan == nil {
		f.plan = recoveryControllerPlan()
	}
	return f.plan.DeepCopy(), f.inspectErr
}

func (f *fakeRecoveryManager) MarkInProgress(
	context.Context,
	*workloadidentityv1alpha1.WorkloadIdentityRecovery,
	*workloadidentityv1alpha1.WorkloadIdentity,
	*workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
) error {
	f.calls = append(f.calls, "mark")
	return f.markErr
}

func (f *fakeRecoveryManager) EnsureFederatedIdentityCredential(
	context.Context,
	*workloadidentityv1alpha1.WorkloadIdentityRecovery,
	*workloadidentityv1alpha1.WorkloadIdentity,
	*workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
) error {
	f.calls = append(f.calls, "fic")
	return f.credentialErr
}

func (f *fakeRecoveryManager) Commit(
	context.Context,
	*workloadidentityv1alpha1.WorkloadIdentityRecovery,
	*workloadidentityv1alpha1.WorkloadIdentity,
	*workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
) error {
	f.calls = append(f.calls, "commit")
	return f.commitErr
}

func (f *fakeRecoveryManager) Finalize(
	context.Context,
	*workloadidentityv1alpha1.WorkloadIdentityRecovery,
	*workloadidentityv1alpha1.WorkloadIdentity,
	*workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan,
) error {
	f.calls = append(f.calls, "finalize")
	return f.finalizeErr
}

func TestWorkloadIdentityRecoveryControllerCompletesForwardRecovery(t *testing.T) {
	ctx := context.Background()
	scheme := recoveryControllerScheme(t)
	identity := recoveryControllerIdentity()
	recovery := recoveryControllerResource()
	serviceAccount := recoveryControllerServiceAccount(string(recoveryControllerPreviousUID))
	manager := &fakeRecoveryManager{}
	kubeClient := recoveryControllerClientBuilder(scheme).
		WithStatusSubresource(
			&workloadidentityv1alpha1.WorkloadIdentity{},
			&workloadidentityv1alpha1.WorkloadIdentityRecovery{},
		).
		WithObjects(identity, recovery, recoveryControllerIssuer(), serviceAccount).
		Build()
	reconciler := &WorkloadIdentityRecoveryReconciler{
		Client:    kubeClient,
		APIReader: kubeClient,
		Scheme:    scheme,
		Manager:   manager,
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: recovery.Name}}

	for range 4 {
		if _, err := reconciler.Reconcile(ctx, request); err != nil {
			t.Fatalf("reconcile through commit: %v", err)
		}
	}
	currentRecovery := getRecovery(t, ctx, kubeClient, recovery.Name)
	if !currentRecovery.Status.CommitVerified || recoveryIsComplete(currentRecovery) {
		t.Fatalf("commit checkpoint = %#v", currentRecovery.Status)
	}
	assertTargetReason(t, ctx, kubeClient, identity, workloadidentity.ReasonRecoveryInProgress)
	assertCalls(t, manager.calls, "inspect", "mark", "fic", "commit")

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("finalize recovery: %v", err)
	}
	currentRecovery = getRecovery(t, ctx, kubeClient, recovery.Name)
	if !recoveryIsComplete(currentRecovery) {
		t.Fatalf("recovery conditions = %#v", currentRecovery.Status.Conditions)
	}
	assertTargetReason(t, ctx, kubeClient, identity, workloadidentity.ReasonRecoveryInProgress)

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("release target: %v", err)
	}
	assertTargetReason(t, ctx, kubeClient, identity, recoveryReasonCompleted)
	assertCalls(t, manager.calls, "inspect", "mark", "fic", "commit", "finalize")

	currentServiceAccount := &corev1.ServiceAccount{}
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(serviceAccount), currentServiceAccount); err != nil {
		t.Fatalf("get ServiceAccount: %v", err)
	}
	if currentServiceAccount.Labels[serviceAccountUID] != string(recoveryControllerCurrentUID) ||
		currentServiceAccount.Annotations[serviceAccountClientID] != "client-id" {
		t.Fatalf("recovered ServiceAccount = %#v", currentServiceAccount.ObjectMeta)
	}
}

func TestWorkloadIdentityRecoveryDeletionBeforeMutationCancels(t *testing.T) {
	ctx := context.Background()
	scheme := recoveryControllerScheme(t)
	identity := recoveryControllerIdentity()
	setRecoveryTargetInProgress(identity)
	recovery := recoveryControllerResource()
	recovery.Finalizers = []string{workloadIdentityRecoveryFinalizer}
	recovery.Status.Plan = recoveryControllerPlan()
	manager := &fakeRecoveryManager{}
	kubeClient := recoveryControllerClientBuilder(scheme).
		WithStatusSubresource(
			&workloadidentityv1alpha1.WorkloadIdentity{},
			&workloadidentityv1alpha1.WorkloadIdentityRecovery{},
		).
		WithObjects(identity, recovery).
		Build()
	if err := kubeClient.Delete(ctx, recovery); err != nil {
		t.Fatalf("delete recovery: %v", err)
	}
	reconciler := &WorkloadIdentityRecoveryReconciler{Client: kubeClient, APIReader: kubeClient, Manager: manager}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: recovery.Name},
	}); err != nil {
		t.Fatalf("reconcile deletion: %v", err)
	}
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(recovery), &workloadidentityv1alpha1.WorkloadIdentityRecovery{}); !apierrors.IsNotFound(err) {
		t.Fatalf("deleted recovery get error = %v", err)
	}
	assertTargetReason(t, ctx, kubeClient, identity, recoveryReasonCancelled)
	assertCalls(t, manager.calls)
}

func TestWorkloadIdentityRecoveryCancellationUsesAuthoritativeTargetRead(t *testing.T) {
	ctx := context.Background()
	scheme := recoveryControllerScheme(t)
	staleIdentity := recoveryControllerIdentity()
	identity := staleIdentity.DeepCopy()
	setRecoveryTargetInProgress(identity)
	recovery := recoveryControllerResource()
	recovery.Finalizers = []string{workloadIdentityRecoveryFinalizer}
	recovery.Status.Plan = recoveryControllerPlan()
	apiClient := recoveryControllerClientBuilder(scheme).
		WithStatusSubresource(
			&workloadidentityv1alpha1.WorkloadIdentity{},
			&workloadidentityv1alpha1.WorkloadIdentityRecovery{},
		).
		WithObjects(identity, recovery).
		Build()
	if err := apiClient.Delete(ctx, recovery); err != nil {
		t.Fatalf("delete recovery: %v", err)
	}
	cachedClient := &staleWorkloadIdentityGetClient{
		Client: apiClient,
		stale:  staleIdentity,
	}
	reconciler := &WorkloadIdentityRecoveryReconciler{
		Client:    cachedClient,
		APIReader: apiClient,
		Manager:   &fakeRecoveryManager{},
	}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: recovery.Name},
	}); err != nil {
		t.Fatalf("reconcile deletion with stale target cache: %v", err)
	}
	if err := apiClient.Get(
		ctx,
		client.ObjectKeyFromObject(recovery),
		&workloadidentityv1alpha1.WorkloadIdentityRecovery{},
	); !apierrors.IsNotFound(err) {
		t.Fatalf("deleted recovery get error = %v", err)
	}
	assertTargetReason(t, ctx, apiClient, identity, recoveryReasonCancelled)
}

func TestWorkloadIdentityRecoveryDeletingDuplicateDoesNotReleaseWinnerTarget(t *testing.T) {
	ctx := context.Background()
	scheme := recoveryControllerScheme(t)
	identity := recoveryControllerIdentity()
	setRecoveryTargetInProgress(identity)
	winner := startedRecoveryControllerResource()
	winner.Name = "winner"
	winner.UID = "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
	winner.CreationTimestamp = metav1.Unix(10, 0)
	duplicate := recoveryControllerResource()
	duplicate.Name = "duplicate"
	duplicate.UID = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	duplicate.CreationTimestamp = metav1.Unix(20, 0)
	duplicate.Finalizers = []string{workloadIdentityRecoveryFinalizer}
	kubeClient := recoveryControllerClientBuilder(scheme).
		WithStatusSubresource(
			&workloadidentityv1alpha1.WorkloadIdentity{},
			&workloadidentityv1alpha1.WorkloadIdentityRecovery{},
		).
		WithObjects(identity, winner, duplicate).
		Build()
	if err := kubeClient.Delete(ctx, duplicate); err != nil {
		t.Fatalf("delete duplicate recovery: %v", err)
	}
	reconciler := &WorkloadIdentityRecoveryReconciler{
		Client:    kubeClient,
		APIReader: kubeClient,
		Manager:   &fakeRecoveryManager{},
	}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: duplicate.Name},
	}); err != nil {
		t.Fatalf("reconcile duplicate deletion: %v", err)
	}
	if err := kubeClient.Get(
		ctx,
		client.ObjectKeyFromObject(duplicate),
		&workloadidentityv1alpha1.WorkloadIdentityRecovery{},
	); !apierrors.IsNotFound(err) {
		t.Fatalf("deleted duplicate recovery get error = %v", err)
	}
	assertTargetReason(t, ctx, kubeClient, identity, workloadidentity.ReasonRecoveryInProgress)
}

func TestWorkloadIdentityRecoveryResumesTargetLockCheckpointDuringDeletion(t *testing.T) {
	ctx := context.Background()
	scheme := recoveryControllerScheme(t)
	identity := recoveryControllerIdentity()
	identity.Finalizers = []string{workloadIdentityFinalizer}
	setRecoveryTargetInProgress(identity)
	recovery := recoveryControllerResource()
	recovery.Finalizers = []string{workloadIdentityRecoveryFinalizer}
	recovery.Status.Plan = recoveryControllerPlan()
	manager := &fakeRecoveryManager{}
	kubeClient := recoveryControllerClientBuilder(scheme).
		WithStatusSubresource(
			&workloadidentityv1alpha1.WorkloadIdentity{},
			&workloadidentityv1alpha1.WorkloadIdentityRecovery{},
		).
		WithObjects(identity, recovery).
		Build()
	if err := kubeClient.Delete(ctx, identity); err != nil {
		t.Fatalf("delete target WorkloadIdentity: %v", err)
	}
	reconciler := &WorkloadIdentityRecoveryReconciler{
		Client:    kubeClient,
		APIReader: kubeClient,
		Manager:   manager,
	}

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: recovery.Name},
	}); err != nil {
		t.Fatalf("resume recovery after target lock checkpoint crash: %v", err)
	}
	current := getRecovery(t, ctx, kubeClient, recovery.Name)
	if !current.Status.MutationStarted || recoveryIsTerminal(current) {
		t.Fatalf("resumed recovery status = %#v", current.Status)
	}
	assertTargetReason(t, ctx, kubeClient, identity, workloadidentity.ReasonRecoveryInProgress)
	assertCalls(t, manager.calls)
}

func TestWorkloadIdentityRecoveryDeletionAfterMutationFinishesForward(t *testing.T) {
	ctx := context.Background()
	scheme := recoveryControllerScheme(t)
	identity := recoveryControllerIdentity()
	setRecoveryTargetInProgress(identity)
	recovery := startedRecoveryControllerResource()
	serviceAccount := recoveryControllerServiceAccount(string(recoveryControllerPreviousUID))
	manager := &fakeRecoveryManager{}
	kubeClient := recoveryControllerClientBuilder(scheme).
		WithStatusSubresource(
			&workloadidentityv1alpha1.WorkloadIdentity{},
			&workloadidentityv1alpha1.WorkloadIdentityRecovery{},
		).
		WithObjects(identity, recovery, serviceAccount).
		Build()
	if err := kubeClient.Delete(ctx, recovery); err != nil {
		t.Fatalf("delete recovery: %v", err)
	}
	reconciler := &WorkloadIdentityRecoveryReconciler{
		Client:    kubeClient,
		APIReader: kubeClient,
		Manager:   manager,
	}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: recovery.Name}}

	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("commit deleting recovery: %v", err)
	}
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatalf("finalize deleting recovery: %v", err)
	}
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(recovery), &workloadidentityv1alpha1.WorkloadIdentityRecovery{}); !apierrors.IsNotFound(err) {
		t.Fatalf("deleted recovery get error = %v", err)
	}
	assertTargetReason(t, ctx, kubeClient, identity, recoveryReasonCompleted)
	assertCalls(t, manager.calls, "mark", "fic", "commit", "finalize")
}

func TestWorkloadIdentityRecoveryBlocksAndRetriesForwardFailure(t *testing.T) {
	ctx := context.Background()
	scheme := recoveryControllerScheme(t)
	identity := recoveryControllerIdentity()
	setRecoveryTargetInProgress(identity)
	recovery := startedRecoveryControllerResource()
	manager := &fakeRecoveryManager{
		credentialErr: workloadidentity.NewRecoveryBlockedError(
			recoveryReasonServiceAccount,
			"external conflict",
		),
	}
	kubeClient := recoveryControllerClientBuilder(scheme).
		WithStatusSubresource(
			&workloadidentityv1alpha1.WorkloadIdentity{},
			&workloadidentityv1alpha1.WorkloadIdentityRecovery{},
		).
		WithObjects(identity, recovery).
		Build()
	reconciler := &WorkloadIdentityRecoveryReconciler{Client: kubeClient, APIReader: kubeClient, Manager: manager}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: recovery.Name},
	})
	if err != nil {
		t.Fatalf("reconcile blocked recovery: %v", err)
	}
	if result.RequeueAfter != recoveryRetryInterval {
		t.Fatalf("requeue = %s, want %s", result.RequeueAfter, recoveryRetryInterval)
	}
	current := getRecovery(t, ctx, kubeClient, recovery.Name)
	if !apimeta.IsStatusConditionTrue(
		current.Status.Conditions,
		string(workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionBlocked),
	) || recoveryIsTerminal(current) {
		t.Fatalf("blocked recovery status = %#v", current.Status)
	}
	assertCalls(t, manager.calls, "mark", "fic")
}

func TestWorkloadIdentityRecoveryAllowsDeletionPolicyChangeAfterStart(t *testing.T) {
	identity := recoveryControllerIdentity()
	setRecoveryTargetInProgress(identity)
	recovery := startedRecoveryControllerResource()
	identity.Spec.DeletionPolicy = workloadidentityv1alpha1.DeletionPolicyDelete
	identity.Generation++

	if err := validateRecoveryControllerTarget(recovery, identity); err != nil {
		t.Fatalf("mutable deletionPolicy blocked forward recovery: %v", err)
	}
}

func TestWorkloadIdentityRecoveryDuplicateStartedRecordWins(t *testing.T) {
	ctx := context.Background()
	scheme := recoveryControllerScheme(t)
	current := recoveryControllerResource()
	current.Name = "newer"
	current.UID = "newer-recovery-uid"
	current.CreationTimestamp = metav1.Unix(20, 0)
	incumbent := startedRecoveryControllerResource()
	incumbent.Name = "older-started"
	incumbent.UID = "older-recovery-uid"
	incumbent.CreationTimestamp = metav1.Unix(10, 0)
	kubeClient := recoveryControllerClientBuilder(scheme).WithObjects(current, incumbent).Build()
	reconciler := &WorkloadIdentityRecoveryReconciler{Client: kubeClient, APIReader: kubeClient}

	winner, err := reconciler.recoveryWinnerForSource(ctx, current)
	if err != nil {
		t.Fatalf("select duplicate winner: %v", err)
	}
	if winner == nil || winner.Name != incumbent.Name {
		t.Fatalf("winner = %#v, want %q", winner, incumbent.Name)
	}
}

func TestWorkloadIdentityRecoveryServiceAccountPatchUsesOptimisticLock(t *testing.T) {
	ctx := context.Background()
	scheme := recoveryControllerScheme(t)
	identity := recoveryControllerIdentity()
	recovery := startedRecoveryControllerResource()
	serviceAccount := recoveryControllerServiceAccount(string(recoveryControllerPreviousUID))
	baseClient := recoveryControllerClientBuilder(scheme).WithObjects(serviceAccount).Build()
	raceClient := &serviceAccountPatchRaceClient{
		Client: baseClient,
		beforePatch: func(context.Context) error {
			return nil
		},
	}
	reconciler := &WorkloadIdentityRecoveryReconciler{Client: raceClient, APIReader: baseClient}

	_, err := reconciler.transferRecoveryServiceAccount(
		ctx,
		recovery,
		identity,
		recovery.Status.Plan,
	)
	if !apierrors.IsConflict(err) {
		t.Fatalf("transfer error = %v, want conflict", err)
	}
	if !raceClient.sawResourceVersion {
		t.Fatal("ServiceAccount patch did not include resourceVersion")
	}
}

func TestActiveRecoveryTargetIndexExcludesTerminalHistory(t *testing.T) {
	active := startedRecoveryControllerResource()
	if got := activeRecoveryTargetIndexValues(active); len(got) != 1 {
		t.Fatalf("active index values = %v", got)
	}
	complete := active.DeepCopy()
	setRecoveryCondition(
		&complete.Status,
		workloadidentityv1alpha1.WorkloadIdentityRecoveryConditionComplete,
		metav1.ConditionTrue,
		recoveryReasonCompleted,
		"complete",
		complete.Generation,
	)
	if got := activeRecoveryTargetIndexValues(complete); got != nil {
		t.Fatalf("completed index values = %v", got)
	}
}

type staleWorkloadIdentityGetClient struct {
	client.Client
	stale *workloadidentityv1alpha1.WorkloadIdentity
}

func (c *staleWorkloadIdentityGetClient) Get(
	ctx context.Context,
	key client.ObjectKey,
	object client.Object,
	options ...client.GetOption,
) error {
	identity, ok := object.(*workloadidentityv1alpha1.WorkloadIdentity)
	if ok && c.stale != nil && key == client.ObjectKeyFromObject(c.stale) {
		c.stale.DeepCopyInto(identity)
		return nil
	}
	return c.Client.Get(ctx, key, object, options...)
}

type serviceAccountPatchRaceClient struct {
	client.Client
	beforePatch        func(context.Context) error
	sawResourceVersion bool
}

func (c *serviceAccountPatchRaceClient) Patch(
	ctx context.Context,
	object client.Object,
	patch client.Patch,
	options ...client.PatchOption,
) error {
	if _, ok := object.(*corev1.ServiceAccount); !ok || c.beforePatch == nil {
		return c.Client.Patch(ctx, object, patch, options...)
	}
	data, err := patch.Data(object)
	if err != nil {
		return err
	}
	c.sawResourceVersion = strings.Contains(string(data), `"resourceVersion"`)
	if err := c.beforePatch(ctx); err != nil {
		return err
	}
	c.beforePatch = nil
	return apierrors.NewConflict(
		schema.GroupResource{Resource: "serviceaccounts"},
		object.GetName(),
		errors.New("simulated concurrent ServiceAccount change"),
	)
}

func recoveryControllerClientBuilder(scheme *runtime.Scheme) *fake.ClientBuilder {
	return fake.NewClientBuilder().WithScheme(scheme).WithIndex(
		&workloadidentityv1alpha1.WorkloadIdentityRecovery{},
		workloadIdentityRecoveryTargetIndex,
		activeRecoveryTargetIndexValues,
	)
}

func recoveryControllerScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := workloadidentityv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add workload identity scheme: %v", err)
	}
	return scheme
}

func recoveryControllerIdentity() *workloadidentityv1alpha1.WorkloadIdentity {
	return &workloadidentityv1alpha1.WorkloadIdentity{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  testWorkloadNamespace,
			Name:       recoveryControllerIdentityName,
			UID:        recoveryControllerCurrentUID,
			Generation: 1,
		},
		Spec: workloadidentityv1alpha1.WorkloadIdentitySpec{
			Azure: workloadidentityv1alpha1.AzureWorkloadIdentityConfig{
				UserAssignedIdentityName:        "example-uami",
				FederatedIdentityCredentialName: "example-fic",
			},
			ServiceAccount: workloadidentityv1alpha1.ServiceAccountReference{Name: "example-sa"},
		},
		Status: workloadidentityv1alpha1.WorkloadIdentityStatus{
			ObservedGeneration: 1,
			Recovery: &workloadidentityv1alpha1.WorkloadIdentityRecoveryRequiredStatus{
				PreviousWorkloadIdentityUID: recoveryControllerPreviousUID,
			},
			Conditions: []metav1.Condition{{
				Type:               string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
				Status:             metav1.ConditionFalse,
				Reason:             workloadidentity.ReasonRecoveryRequired,
				ObservedGeneration: 1,
			}},
		},
	}
}

func recoveryControllerResource() *workloadidentityv1alpha1.WorkloadIdentityRecovery {
	return &workloadidentityv1alpha1.WorkloadIdentityRecovery{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "recover-example",
			UID:        "dddddddd-dddd-dddd-dddd-dddddddddddd",
			Generation: 1,
		},
		Spec: workloadidentityv1alpha1.WorkloadIdentityRecoverySpec{
			WorkloadIdentityRef: workloadidentityv1alpha1.WorkloadIdentityRecoveryReference{
				Namespace: testWorkloadNamespace,
				Name:      recoveryControllerIdentityName,
				UID:       recoveryControllerCurrentUID,
			},
			PreviousWorkloadIdentityUID: recoveryControllerPreviousUID,
		},
	}
}

func startedRecoveryControllerResource() *workloadidentityv1alpha1.WorkloadIdentityRecovery {
	recovery := recoveryControllerResource()
	recovery.Finalizers = []string{workloadIdentityRecoveryFinalizer}
	recovery.Status.MutationStarted = true
	recovery.Status.Plan = recoveryControllerPlan()
	return recovery
}

func recoveryControllerIssuer() *workloadidentityv1alpha1.OIDCIssuer {
	return &workloadidentityv1alpha1.OIDCIssuer{
		ObjectMeta: metav1.ObjectMeta{Name: workloadidentityv1alpha1.OIDCIssuerName},
		Status: workloadidentityv1alpha1.OIDCIssuerStatus{
			IssuerURL: "https://issuer.example",
			Conditions: []metav1.Condition{{
				Type:   string(workloadidentityv1alpha1.OIDCIssuerConditionReady),
				Status: metav1.ConditionTrue,
			}},
		},
	}
}

func recoveryControllerServiceAccount(ownerUID string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       testWorkloadNamespace,
			Name:            "example-sa",
			UID:             recoveryControllerSAUID,
			ResourceVersion: "1",
			Labels: map[string]string{
				serviceAccountUseLabel:  trueValue,
				serviceAccountManagedBy: serviceAccountManagerName,
				serviceAccountUID:       ownerUID,
				serviceAccountCreatedBy: trueValue,
			},
			Annotations: map[string]string{
				serviceAccountClientID: "old-client-id",
				serviceAccountTenantID: "tenant-id",
			},
		},
	}
}

func recoveryControllerPlan() *workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan {
	return &workloadidentityv1alpha1.WorkloadIdentityRecoveryPlan{
		UserAssignedIdentity: workloadidentityv1alpha1.WorkloadIdentityRecoveryUserAssignedIdentity{
			ID:          "uami-id",
			ClientID:    "client-id",
			PrincipalID: "principal-id",
			TenantID:    "tenant-id",
		},
		FederatedIdentityCredential: workloadidentityv1alpha1.WorkloadIdentityRecoveryFederatedIdentityCredential{
			Issuer:    "https://issuer.example",
			Subject:   "system:serviceaccount:default:example-sa",
			Audiences: []string{"api://AzureADTokenExchange"},
		},
	}
}

func setRecoveryTargetInProgress(identity *workloadidentityv1alpha1.WorkloadIdentity) {
	apimeta.SetStatusCondition(&identity.Status.Conditions, metav1.Condition{
		Type:               string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
		Status:             metav1.ConditionFalse,
		Reason:             workloadidentity.ReasonRecoveryInProgress,
		ObservedGeneration: identity.Generation,
	})
}

func assertTargetReason(
	t *testing.T,
	ctx context.Context,
	kubeClient client.Client,
	identity *workloadidentityv1alpha1.WorkloadIdentity,
	reason string,
) {
	t.Helper()
	current := &workloadidentityv1alpha1.WorkloadIdentity{}
	if err := kubeClient.Get(ctx, client.ObjectKeyFromObject(identity), current); err != nil {
		t.Fatalf("get target: %v", err)
	}
	ready := apimeta.FindStatusCondition(
		current.Status.Conditions,
		string(workloadidentityv1alpha1.WorkloadIdentityConditionReady),
	)
	if ready == nil || ready.Reason != reason {
		t.Fatalf("target Ready condition = %#v, want reason %q", ready, reason)
	}
}

func getRecovery(
	t *testing.T,
	ctx context.Context,
	kubeClient client.Client,
	name string,
) *workloadidentityv1alpha1.WorkloadIdentityRecovery {
	t.Helper()
	current := &workloadidentityv1alpha1.WorkloadIdentityRecovery{}
	if err := kubeClient.Get(ctx, client.ObjectKey{Name: name}, current); err != nil {
		t.Fatalf("get recovery: %v", err)
	}
	return current
}

func assertCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slicesEqual(got, want) {
		t.Fatalf("manager calls = %v, want %v", got, want)
	}
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
