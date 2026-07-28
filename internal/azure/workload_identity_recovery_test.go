package azure

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

const recoveryTestSourceUID types.UID = "earlier-uid"

func TestWorkloadIdentityRecoveryManagerKeepsFenceThroughCommit(t *testing.T) {
	identity, recovery, manager, set := recoveryManagerTest(t)
	ctx := context.Background()

	plan, err := manager.Inspect(ctx, recovery, identity, testIssuerURL, testSubject)
	if err != nil {
		t.Fatalf("inspect recovery: %v", err)
	}
	if set.uami.updates != 0 || set.fic.puts != 0 {
		t.Fatal("recovery preflight mutated Azure")
	}
	if err := manager.MarkInProgress(ctx, recovery, identity, plan); err != nil {
		t.Fatalf("mark recovery in progress: %v", err)
	}
	if err := manager.EnsureFederatedIdentityCredential(ctx, recovery, identity, plan); err != nil {
		t.Fatalf("repair federated identity credential: %v", err)
	}
	if err := manager.Commit(ctx, recovery, identity, plan); err != nil {
		t.Fatalf("commit recovery: %v", err)
	}

	if got := tagValue(set.uami.identity.Tags, workloadIdentityUIDTag); got != string(identity.UID) {
		t.Fatalf("owner UID = %q, want %q", got, identity.UID)
	}
	if got := tagValue(set.uami.identity.Tags, workloadIdentityLastRecoveryUIDTag); got != string(recovery.UID) {
		t.Fatalf("last recovery UID = %q, want %q", got, recovery.UID)
	}
	if got := tagValue(set.uami.identity.Tags, workloadIdentityRecoveryUIDTag); got != string(recovery.UID) {
		t.Fatalf("recovery UID fence = %q, want %q", got, recovery.UID)
	}
	if got := tagValue(set.uami.identity.Tags, workloadIdentityRecoveryTargetUIDTag); got != string(identity.UID) {
		t.Fatalf("target UID fence = %q, want %q", got, identity.UID)
	}
	if reason, ok := workloadidentity.ConflictReason(
		validateUserAssignedIdentityOwnership(identity, set.uami.identity),
	); !ok || reason != workloadidentity.ReasonRecoveryInProgress {
		t.Fatalf("normal ownership validation reason = %q, conflict = %t", reason, ok)
	}

	updatesAfterCommit := set.uami.updates
	if err := manager.Commit(ctx, recovery, identity, plan); err != nil {
		t.Fatalf("retry committed recovery: %v", err)
	}
	if set.uami.updates != updatesAfterCommit {
		t.Fatalf("committed retry performed an Azure write")
	}

	if err := manager.Finalize(ctx, recovery, identity, plan); err != nil {
		t.Fatalf("finalize recovery: %v", err)
	}
	if tagValue(set.uami.identity.Tags, workloadIdentityRecoveryUIDTag) != "" ||
		tagValue(set.uami.identity.Tags, workloadIdentityRecoveryTargetUIDTag) != "" {
		t.Fatal("recovery fence remained after finalization")
	}
	if err := validateUserAssignedIdentityOwnership(identity, set.uami.identity); err != nil {
		t.Fatalf("normal ownership validation after finalization: %v", err)
	}
}

func TestWorkloadIdentityRecoveryManagerRejectsAmbiguousFICsWithoutMutation(t *testing.T) {
	identity, recovery, manager, set := recoveryManagerTest(t)
	set.fic.credentials = []armmsi.FederatedIdentityCredential{
		{Name: to.Ptr("fic-one")},
		{Name: to.Ptr("fic-two")},
	}

	_, err := manager.Inspect(context.Background(), recovery, identity, testIssuerURL, testSubject)
	if reason, ok := workloadidentity.RecoveryBlockedReason(err); !ok ||
		reason != recoveryReasonFederatedIdentityCredentialAmbiguous {
		t.Fatalf("blocked reason = %q, %t; error: %v", reason, ok, err)
	}
	if set.uami.updates != 0 || set.fic.puts != 0 || set.fic.deletes != 0 {
		t.Fatal("ambiguous FIC preflight mutated Azure")
	}
}

func TestWorkloadIdentityRecoveryManagerKeepsFenceWhenCommitBecomesAmbiguous(t *testing.T) {
	identity, recovery, manager, set := recoveryManagerTest(t)
	ctx := context.Background()
	plan, err := manager.Inspect(ctx, recovery, identity, testIssuerURL, testSubject)
	if err != nil {
		t.Fatalf("inspect recovery: %v", err)
	}
	if err := manager.MarkInProgress(ctx, recovery, identity, plan); err != nil {
		t.Fatalf("mark recovery in progress: %v", err)
	}
	if err := manager.EnsureFederatedIdentityCredential(ctx, recovery, identity, plan); err != nil {
		t.Fatalf("repair federated identity credential: %v", err)
	}
	set.fic.credentials = []armmsi.FederatedIdentityCredential{
		set.fic.credential,
		foreignRecoveryFederatedIdentityCredential(),
	}

	err = manager.Commit(ctx, recovery, identity, plan)
	if reason, ok := workloadidentity.RecoveryBlockedReason(err); !ok ||
		reason != recoveryReasonFederatedIdentityCredentialAmbiguous {
		t.Fatalf("commit error = %v, reason = %q, blocked = %t", err, reason, ok)
	}
	if got := tagValue(set.uami.identity.Tags, workloadIdentityUIDTag); got != string(recoveryTestSourceUID) {
		t.Fatalf("owner UID = %q, want source UID %q", got, recoveryTestSourceUID)
	}
	if got := tagValue(set.uami.identity.Tags, workloadIdentityRecoveryUIDTag); got != string(recovery.UID) {
		t.Fatalf("recovery fence = %q, want %q", got, recovery.UID)
	}
}

func TestWorkloadIdentityRecoveryManagerDoesNotClearFenceWhenFinalizeBecomesAmbiguous(t *testing.T) {
	identity, recovery, manager, set := recoveryManagerTest(t)
	ctx := context.Background()
	plan, err := manager.Inspect(ctx, recovery, identity, testIssuerURL, testSubject)
	if err != nil {
		t.Fatalf("inspect recovery: %v", err)
	}
	if err := manager.MarkInProgress(ctx, recovery, identity, plan); err != nil {
		t.Fatalf("mark recovery in progress: %v", err)
	}
	if err := manager.EnsureFederatedIdentityCredential(ctx, recovery, identity, plan); err != nil {
		t.Fatalf("repair federated identity credential: %v", err)
	}
	if err := manager.Commit(ctx, recovery, identity, plan); err != nil {
		t.Fatalf("commit recovery: %v", err)
	}
	set.fic.credentials = []armmsi.FederatedIdentityCredential{
		set.fic.credential,
		foreignRecoveryFederatedIdentityCredential(),
	}

	err = manager.Finalize(ctx, recovery, identity, plan)
	if reason, ok := workloadidentity.RecoveryBlockedReason(err); !ok ||
		reason != recoveryReasonFederatedIdentityCredentialAmbiguous {
		t.Fatalf("finalize error = %v, reason = %q, blocked = %t", err, reason, ok)
	}
	if got := tagValue(set.uami.identity.Tags, workloadIdentityRecoveryUIDTag); got != string(recovery.UID) {
		t.Fatalf("recovery fence = %q, want %q", got, recovery.UID)
	}
}

func recoveryManagerTest(
	t *testing.T,
) (
	*workloadidentityv1alpha1.WorkloadIdentity,
	*workloadidentityv1alpha1.WorkloadIdentityRecovery,
	*WorkloadIdentityRecoveryManager,
	*fakeIdentityClientSet,
) {
	t.Helper()
	identity := managedTestWorkloadIdentity()
	set := testClientSet(withTag(
		workloadIdentityTags(identity),
		workloadIdentityUIDTag,
		string(recoveryTestSourceUID),
	))
	set.fic.credential.Name = to.Ptr(identity.Spec.Azure.FederatedIdentityCredentialName)
	recovery := testRecovery(identity)
	manager := &WorkloadIdentityRecoveryManager{
		clientsFactory: func() (*identityClients, error) { return set.clients, nil },
	}
	return identity, recovery, manager, set
}

func foreignRecoveryFederatedIdentityCredential() armmsi.FederatedIdentityCredential {
	return armmsi.FederatedIdentityCredential{
		ID:   to.Ptr(testFICID + "-foreign"),
		Name: to.Ptr("foreign-fic"),
		Properties: &armmsi.FederatedIdentityCredentialProperties{
			Issuer:    to.Ptr(testIssuerURL),
			Subject:   to.Ptr("foreign-subject"),
			Audiences: []*string{to.Ptr(azureADTokenExchangeAudience)},
		},
	}
}

func testRecovery(identity *workloadidentityv1alpha1.WorkloadIdentity) *workloadidentityv1alpha1.WorkloadIdentityRecovery {
	return &workloadidentityv1alpha1.WorkloadIdentityRecovery{
		ObjectMeta: metav1.ObjectMeta{
			Name: "recover-test",
			UID:  "recovery-uid",
		},
		Spec: workloadidentityv1alpha1.WorkloadIdentityRecoverySpec{
			WorkloadIdentityRef: workloadidentityv1alpha1.WorkloadIdentityRecoveryReference{
				Namespace: identity.Namespace,
				Name:      identity.Name,
				UID:       identity.UID,
			},
			PreviousWorkloadIdentityUID: recoveryTestSourceUID,
		},
	}
}
