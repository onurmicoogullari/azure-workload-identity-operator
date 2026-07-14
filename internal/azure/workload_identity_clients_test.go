package azure

import (
	"context"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

const (
	testResourceGroupID = "/subscriptions/test/resourceGroups/rg-test"
	testUAMIID          = testResourceGroupID + "/providers/Microsoft.ManagedIdentity/userAssignedIdentities/uami-test"
	testOtherUAMIID     = testResourceGroupID + "/providers/Microsoft.ManagedIdentity/userAssignedIdentities/uami-other"
	testFICID           = testUAMIID + "/federatedIdentityCredentials/fic-test"
	testOtherUID        = "other-uid"
	testAzureClientID   = "11111111-1111-1111-1111-111111111111"
	testPrincipalID     = "22222222-2222-2222-2222-222222222222"
	testTenantID        = "33333333-3333-3333-3333-333333333333"
	testIssuerURL       = "https://issuer.example"
	testSubject         = "system:serviceaccount:default:test-sa"
)

type fakeResourceGroupsClient struct {
	resourceGroup armresources.ResourceGroup
	getErr        error
	deleteErr     error
	gets          int
	puts          int
	deletes       int
}

func (f *fakeResourceGroupsClient) Get(context.Context, string, *armresources.ResourceGroupsClientGetOptions) (armresources.ResourceGroupsClientGetResponse, error) {
	f.gets++
	return armresources.ResourceGroupsClientGetResponse{ResourceGroup: f.resourceGroup}, f.getErr
}

func (f *fakeResourceGroupsClient) CreateOrUpdate(_ context.Context, _ string, resourceGroup armresources.ResourceGroup, _ *armresources.ResourceGroupsClientCreateOrUpdateOptions) (armresources.ResourceGroupsClientCreateOrUpdateResponse, error) {
	f.puts++
	if resourceGroup.ID == nil {
		resourceGroup.ID = f.resourceGroup.ID
	}
	f.resourceGroup = resourceGroup
	return armresources.ResourceGroupsClientCreateOrUpdateResponse{ResourceGroup: resourceGroup}, nil
}

func (f *fakeResourceGroupsClient) Delete(context.Context, string) error {
	f.deletes++
	return f.deleteErr
}

type fakeUserAssignedIdentitiesClient struct {
	identity armmsi.Identity
	getErr   error
	gets     int
	puts     int
	updates  int
	deletes  int
}

func (f *fakeUserAssignedIdentitiesClient) Get(context.Context, string, string, *armmsi.UserAssignedIdentitiesClientGetOptions) (armmsi.UserAssignedIdentitiesClientGetResponse, error) {
	f.gets++
	return armmsi.UserAssignedIdentitiesClientGetResponse{Identity: f.identity}, f.getErr
}

func (f *fakeUserAssignedIdentitiesClient) CreateOrUpdate(_ context.Context, _, _ string, identity armmsi.Identity, _ *armmsi.UserAssignedIdentitiesClientCreateOrUpdateOptions) (armmsi.UserAssignedIdentitiesClientCreateOrUpdateResponse, error) {
	f.puts++
	f.identity = identity
	return armmsi.UserAssignedIdentitiesClientCreateOrUpdateResponse{Identity: identity}, nil
}

func (f *fakeUserAssignedIdentitiesClient) Update(_ context.Context, _, _ string, update armmsi.IdentityUpdate, _ *armmsi.UserAssignedIdentitiesClientUpdateOptions) (armmsi.UserAssignedIdentitiesClientUpdateResponse, error) {
	f.updates++
	f.identity.Location = update.Location
	f.identity.Tags = update.Tags
	return armmsi.UserAssignedIdentitiesClientUpdateResponse{Identity: f.identity}, nil
}

func (f *fakeUserAssignedIdentitiesClient) Delete(context.Context, string, string, *armmsi.UserAssignedIdentitiesClientDeleteOptions) (armmsi.UserAssignedIdentitiesClientDeleteResponse, error) {
	f.deletes++
	return armmsi.UserAssignedIdentitiesClientDeleteResponse{}, nil
}

type fakeFederatedIdentityCredentialsClient struct {
	credential armmsi.FederatedIdentityCredential
	getErr     error
	gets       int
	puts       int
	deletes    int
}

func (f *fakeFederatedIdentityCredentialsClient) Get(context.Context, string, string, string, *armmsi.FederatedIdentityCredentialsClientGetOptions) (armmsi.FederatedIdentityCredentialsClientGetResponse, error) {
	f.gets++
	return armmsi.FederatedIdentityCredentialsClientGetResponse{FederatedIdentityCredential: f.credential}, f.getErr
}

func (f *fakeFederatedIdentityCredentialsClient) CreateOrUpdate(_ context.Context, _, _, _ string, credential armmsi.FederatedIdentityCredential, _ *armmsi.FederatedIdentityCredentialsClientCreateOrUpdateOptions) (armmsi.FederatedIdentityCredentialsClientCreateOrUpdateResponse, error) {
	f.puts++
	f.credential = credential
	return armmsi.FederatedIdentityCredentialsClientCreateOrUpdateResponse{FederatedIdentityCredential: credential}, nil
}

func (f *fakeFederatedIdentityCredentialsClient) Delete(context.Context, string, string, string, *armmsi.FederatedIdentityCredentialsClientDeleteOptions) (armmsi.FederatedIdentityCredentialsClientDeleteResponse, error) {
	f.deletes++
	return armmsi.FederatedIdentityCredentialsClientDeleteResponse{}, nil
}

type fakeIdentityClientSet struct {
	clients *identityClients
	rg      *fakeResourceGroupsClient
	uami    *fakeUserAssignedIdentitiesClient
	fic     *fakeFederatedIdentityCredentialsClient
}

func TestPeriodicEnsurePerformsNoAzureWritesWithoutDrift(t *testing.T) {
	tests := []struct {
		name string
		tags map[string]*string
	}{
		{name: "operator-created", tags: workloadIdentityTags(managedTestWorkloadIdentity(), true)},
		{name: "adopted with extra user tag", tags: mergeTags(workloadIdentityTags(managedTestWorkloadIdentity(), false), map[string]*string{"user-tag": to.Ptr("preserved")})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity := managedTestWorkloadIdentity()
			set := testClientSet(identity, tt.tags, tt.tags)

			for range 2 {
				_, parent, err := set.clients.ensureUserAssignedIdentity(context.Background(), identity)
				if err != nil {
					t.Fatalf("ensure user assigned identity: %v", err)
				}
				if _, err := set.clients.ensureFederatedIdentityCredential(context.Background(), identity, parent, testIssuerURL, testSubject); err != nil {
					t.Fatalf("ensure federated identity credential: %v", err)
				}
			}

			if set.rg.puts != 0 || set.uami.puts != 0 || set.uami.updates != 0 || set.fic.puts != 0 {
				t.Fatalf("unexpected Azure writes: resourceGroups=%d identityCreates=%d identityUpdates=%d credentials=%d", set.rg.puts, set.uami.puts, set.uami.updates, set.fic.puts)
			}
			if set.rg.gets != 2 || set.uami.gets != 2 || set.fic.gets != 2 {
				t.Fatalf("Azure reads = (%d,%d,%d), want two complete cycles", set.rg.gets, set.uami.gets, set.fic.gets)
			}
		})
	}
}

func TestEnsurePreservesSharedResourceGroupAndUserAssignedIdentity(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	otherIdentity := managedTestWorkloadIdentity()
	otherIdentity.UID = testOtherUID
	set := testClientSet(identity, workloadIdentityTags(otherIdentity, true), workloadIdentityTags(otherIdentity, true))

	_, parent, err := set.clients.ensureUserAssignedIdentity(context.Background(), identity)
	if err != nil {
		t.Fatalf("ensure shared user assigned identity: %v", err)
	}
	if _, err := set.clients.ensureFederatedIdentityCredential(context.Background(), identity, parent, testIssuerURL, testSubject); err != nil {
		t.Fatalf("reuse matching federated identity credential: %v", err)
	}
	if set.rg.puts != 0 || set.uami.updates != 0 || set.fic.puts != 0 {
		t.Fatalf("unexpected shared-parent writes: resourceGroups=%d identities=%d credentials=%d", set.rg.puts, set.uami.updates, set.fic.puts)
	}
}

func TestFirstReconcileAdoptsUnmanagedUserAssignedIdentity(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	set := testClientSet(identity, workloadIdentityTags(identity, true), nil)

	_, parent, err := set.clients.ensureUserAssignedIdentity(context.Background(), identity)
	if err != nil {
		t.Fatalf("ensure user assigned identity: %v", err)
	}
	if set.uami.updates != 1 {
		t.Fatalf("identity updates = %d, want 1", set.uami.updates)
	}
	if !isResourceOwnedByWorkloadIdentity(parent.Tags, identity) {
		t.Fatal("expected adopted identity to receive WorkloadIdentity ownership tags")
	}
}

func TestRecordedUserAssignedIdentityContinuityUsesExactIDAndIdentityProperties(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	recordManagedIdentityStatus(identity)

	t.Run("same path with replacement properties is rejected", func(t *testing.T) {
		set := testClientSet(identity, workloadIdentityTags(identity, true), nil)
		set.uami.identity.Properties.ClientID = to.Ptr("replacement-client-id")

		_, _, err := set.clients.ensureUserAssignedIdentity(context.Background(), identity)
		assertOwnershipConflict(t, err)
		if set.uami.updates != 0 {
			t.Fatalf("identity updates = %d, want 0", set.uami.updates)
		}
	})

	t.Run("different path remains adoptable", func(t *testing.T) {
		set := testClientSet(identity, workloadIdentityTags(identity, true), nil)
		set.uami.identity.ID = to.Ptr(testOtherUAMIID)

		_, _, err := set.clients.ensureUserAssignedIdentity(context.Background(), identity)
		if err != nil {
			t.Fatalf("adopt different identity path: %v", err)
		}
		if set.uami.updates != 1 {
			t.Fatalf("identity updates = %d, want 1", set.uami.updates)
		}
	})
}

func TestRecordedUserAssignedIdentityIsNotRecreatedAfterNotFound(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	recordManagedIdentityStatus(identity)
	set := testClientSet(identity, workloadIdentityTags(identity, true), workloadIdentityTags(identity, true))
	set.uami.getErr = notFoundResponseError()

	_, _, err := set.clients.ensureUserAssignedIdentity(context.Background(), identity)
	assertOwnershipConflict(t, err)
	if set.uami.puts != 0 {
		t.Fatalf("identity creates = %d, want 0", set.uami.puts)
	}
}

func TestEnsureRepairsFederatedCredentialForOwnedOrContinuousSharedParent(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "owned parent"
		if shared {
			name = "continuous shared parent"
		}
		t.Run(name, func(t *testing.T) {
			identity := managedTestWorkloadIdentity()
			recordManagedIdentityStatus(identity)
			identity.Status.IssuerURL = "https://old-issuer.example"
			identity.Status.Subject = testSubject
			parentTags := workloadIdentityTags(identity, true)
			if shared {
				otherIdentity := managedTestWorkloadIdentity()
				otherIdentity.UID = testOtherUID
				parentTags = workloadIdentityTags(otherIdentity, true)
			}
			set := testClientSet(identity, workloadIdentityTags(identity, true), parentTags)
			set.fic.credential = desiredFederatedIdentityCredential(identity.Status.IssuerURL, identity.Status.Subject)
			set.fic.credential.ID = to.Ptr(testFICID)

			if _, err := set.clients.ensureFederatedIdentityCredential(context.Background(), identity, set.uami.identity, testIssuerURL, testSubject); err != nil {
				t.Fatalf("repair federated identity credential: %v", err)
			}
			if set.fic.puts != 1 {
				t.Fatalf("credential writes = %d, want 1", set.fic.puts)
			}
		})
	}
}

func TestEnsureDoesNotRepairDifferentFederatedCredentialPathFromHistoricalTuple(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	recordManagedIdentityStatus(identity)
	identity.Status.IssuerURL = "https://old-issuer.example"
	identity.Status.Subject = testSubject
	set := testClientSet(identity, workloadIdentityTags(identity, true), workloadIdentityTags(identity, true))
	set.fic.credential = desiredFederatedIdentityCredential(identity.Status.IssuerURL, identity.Status.Subject)
	set.fic.credential.ID = to.Ptr(testUAMIID + "/federatedIdentityCredentials/other-fic")

	_, err := set.clients.ensureFederatedIdentityCredential(context.Background(), identity, set.uami.identity, testIssuerURL, testSubject)
	reason, ok := workloadidentity.ConflictReason(err)
	if !ok || reason != workloadidentity.ReasonFederatedIdentityCredentialConflict {
		t.Fatalf("expected federated identity credential conflict, got %v", err)
	}
	if set.fic.puts != 0 {
		t.Fatalf("credential writes = %d, want 0", set.fic.puts)
	}
}

func TestFirstReconcileCanCreateFederatedCredentialUnderSharedParent(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	otherIdentity := managedTestWorkloadIdentity()
	otherIdentity.UID = testOtherUID
	set := testClientSet(identity, workloadIdentityTags(otherIdentity, true), workloadIdentityTags(otherIdentity, true))
	set.fic.getErr = notFoundResponseError()

	if _, err := set.clients.ensureFederatedIdentityCredential(context.Background(), identity, set.uami.identity, testIssuerURL, testSubject); err != nil {
		t.Fatalf("create federated identity credential: %v", err)
	}
	if set.fic.puts != 1 {
		t.Fatalf("credential writes = %d, want 1", set.fic.puts)
	}
}

func TestDeleteOwnedResources(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	recordManagedIdentityStatus(identity)
	identity.Status.IssuerURL = testIssuerURL
	identity.Status.Subject = testSubject
	set := testClientSet(identity, workloadIdentityTags(identity, true), workloadIdentityTags(identity, true))

	if err := set.clients.delete(context.Background(), identity, workloadidentity.DeleteOptions{}); err != nil {
		t.Fatalf("delete owned resources: %v", err)
	}
	assertDeleteCounts(t, set, 1, 1, 1)
}

func TestDeleteAuthorizedCredentialRetainsAdoptedParents(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	recordManagedIdentityStatus(identity)
	identity.Status.IssuerURL = testIssuerURL
	identity.Status.Subject = testSubject
	set := testClientSet(identity, workloadIdentityTags(identity, false), workloadIdentityTags(identity, false))

	if err := set.clients.delete(context.Background(), identity, workloadidentity.DeleteOptions{}); err != nil {
		t.Fatalf("delete credential under adopted parent: %v", err)
	}
	assertDeleteCounts(t, set, 1, 0, 0)
}

func TestDeletePreservesSharedParents(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	recordManagedIdentityStatus(identity)
	identity.Status.IssuerURL = testIssuerURL
	identity.Status.Subject = testSubject
	set := testClientSet(identity, workloadIdentityTags(identity, true), workloadIdentityTags(identity, true))
	options := workloadidentity.DeleteOptions{PreserveResourceGroup: true, PreserveUserAssignedIdentity: true}

	if err := set.clients.delete(context.Background(), identity, options); err != nil {
		t.Fatalf("delete credential with shared parents: %v", err)
	}
	assertDeleteCounts(t, set, 1, 0, 0)
}

func TestDeleteUnmanagedFirstTimeParentMakesNoChanges(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	set := testClientSet(identity, workloadIdentityTags(identity, true), nil)

	if err := set.clients.delete(context.Background(), identity, workloadidentity.DeleteOptions{}); err != nil {
		t.Fatalf("delete with unmanaged parent: %v", err)
	}
	assertDeleteCounts(t, set, 0, 0, 0)
}

func TestDeleteUnsafeParentTransfersResourceGroupWithoutDeletionProvenance(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	set := testClientSet(identity, workloadIdentityTags(identity, true), nil)
	options := workloadidentity.DeleteOptions{PreserveResourceGroup: true, ResourceGroupSuccessorUID: testOtherUID}

	if err := set.clients.delete(context.Background(), identity, options); err != nil {
		t.Fatalf("preserve resource group for unsafe parent: %v", err)
	}
	successor := managedTestWorkloadIdentity()
	successor.UID = testOtherUID
	if wasWorkloadIdentityCreatedByOperator(set.rg.resourceGroup.Tags, successor) {
		t.Fatal("unsafe parent transfer retained resource-group deletion provenance")
	}
	if err := set.clients.deleteResourceGroupIfOwned(context.Background(), successor); err != nil {
		t.Fatalf("evaluate successor resource-group cleanup: %v", err)
	}
	assertDeleteCounts(t, set, 0, 0, 0)
}

func TestDeleteRejectsRecordedReplacement(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	recordManagedIdentityStatus(identity)
	set := testClientSet(identity, workloadIdentityTags(identity, true), nil)
	set.uami.identity.Properties.ClientID = to.Ptr("replacement-client-id")

	err := set.clients.delete(context.Background(), identity, workloadidentity.DeleteOptions{})
	assertOwnershipConflict(t, err)
	assertDeleteCounts(t, set, 0, 0, 0)
}

func TestDeleteRetainsSameTupleCredentialAtDifferentRecordedID(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	recordManagedIdentityStatus(identity)
	identity.Status.IssuerURL = testIssuerURL
	identity.Status.Subject = testSubject
	set := testClientSet(identity, workloadIdentityTags(identity, true), workloadIdentityTags(identity, true))
	set.fic.credential.ID = to.Ptr(testUAMIID + "/federatedIdentityCredentials/other-fic")

	if err := set.clients.delete(context.Background(), identity, workloadidentity.DeleteOptions{}); err != nil {
		t.Fatalf("retain different credential path: %v", err)
	}
	assertDeleteCounts(t, set, 0, 0, 0)
}

func TestDeleteUnsafeCredentialTransfersResourceGroupWithoutDeletionProvenance(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	recordManagedIdentityStatus(identity)
	identity.Status.IssuerURL = testIssuerURL
	identity.Status.Subject = testSubject
	set := testClientSet(identity, workloadIdentityTags(identity, true), workloadIdentityTags(identity, true))
	set.fic.credential.ID = to.Ptr(testUAMIID + "/federatedIdentityCredentials/other-fic")
	options := workloadidentity.DeleteOptions{PreserveResourceGroup: true, ResourceGroupSuccessorUID: testOtherUID}

	if err := set.clients.delete(context.Background(), identity, options); err != nil {
		t.Fatalf("preserve resource group for unsafe credential: %v", err)
	}
	successor := managedTestWorkloadIdentity()
	successor.UID = testOtherUID
	if wasWorkloadIdentityCreatedByOperator(set.rg.resourceGroup.Tags, successor) {
		t.Fatal("unsafe credential transfer retained resource-group deletion provenance")
	}
	if err := set.clients.deleteResourceGroupIfOwned(context.Background(), successor); err != nil {
		t.Fatalf("evaluate successor resource-group cleanup: %v", err)
	}
	assertDeleteCounts(t, set, 0, 0, 0)
}

func TestDeleteCleansUpOwnedResourceGroupWhenUserAssignedIdentityIsGone(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	set := testClientSet(identity, workloadIdentityTags(identity, true), workloadIdentityTags(identity, true))
	set.uami.getErr = notFoundResponseError()

	if err := set.clients.delete(context.Background(), identity, workloadidentity.DeleteOptions{}); err != nil {
		t.Fatalf("delete resource group: %v", err)
	}
	assertDeleteCounts(t, set, 0, 0, 1)
}

func TestDeleteTransfersPreservedResourceGroupWhenUserAssignedIdentityIsGone(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	set := testClientSet(identity, workloadIdentityTags(identity, true), workloadIdentityTags(identity, true))
	set.uami.getErr = notFoundResponseError()
	options := workloadidentity.DeleteOptions{PreserveResourceGroup: true, ResourceGroupSuccessorUID: testOtherUID}

	if err := set.clients.delete(context.Background(), identity, options); err != nil {
		t.Fatalf("transfer resource group ownership: %v", err)
	}
	if !hasTags(set.rg.resourceGroup.Tags, workloadIdentityTagsForUID(testOtherUID, true)) {
		t.Fatal("resource group ownership was not transferred")
	}
	assertDeleteCounts(t, set, 0, 0, 0)
}

func TestSequentialSharedParentDeletionTransfersOwnershipToSuccessor(t *testing.T) {
	owner := managedTestWorkloadIdentity()
	recordManagedIdentityStatus(owner)
	owner.Status.IssuerURL = testIssuerURL
	owner.Status.Subject = testSubject
	set := testClientSet(owner, workloadIdentityTags(owner, true), workloadIdentityTags(owner, true))
	options := workloadidentity.DeleteOptions{
		PreserveResourceGroup:            true,
		PreserveUserAssignedIdentity:     true,
		ResourceGroupSuccessorUID:        testOtherUID,
		UserAssignedIdentitySuccessorUID: testOtherUID,
	}

	if err := set.clients.delete(context.Background(), owner, options); err != nil {
		t.Fatalf("delete original owner: %v", err)
	}
	if !hasTags(set.rg.resourceGroup.Tags, workloadIdentityTagsForUID(testOtherUID, true)) {
		t.Fatal("resource group ownership was not transferred")
	}
	if !hasTags(set.uami.identity.Tags, workloadIdentityTagsForUID(testOtherUID, true)) {
		t.Fatal("user assigned identity ownership was not transferred")
	}

	successor := managedTestWorkloadIdentity()
	successor.UID = testOtherUID
	successor.Spec.Azure.FederatedIdentityCredentialName = "fic-successor"
	recordManagedIdentityStatus(successor)
	successor.Status.IssuerURL = testIssuerURL
	successor.Status.Subject = testSubject
	successor.Status.AzureResources[2].ID = testUAMIID + "/federatedIdentityCredentials/fic-successor"
	set.fic.credential = desiredFederatedIdentityCredential(testIssuerURL, testSubject)
	set.fic.credential.ID = to.Ptr(successor.Status.AzureResources[2].ID)

	if err := set.clients.delete(context.Background(), successor, workloadidentity.DeleteOptions{}); err != nil {
		t.Fatalf("delete successor: %v", err)
	}
	assertDeleteCounts(t, set, 2, 1, 1)
}

func testClientSet(identity *azworkloadidentityv1alpha1.WorkloadIdentity, resourceGroupTags, identityTags map[string]*string) *fakeIdentityClientSet {
	rgClient := &fakeResourceGroupsClient{resourceGroup: armresources.ResourceGroup{
		ID:       to.Ptr(testResourceGroupID),
		Location: to.Ptr(identity.Spec.Azure.Location),
		Tags:     resourceGroupTags,
	}}
	uamiClient := &fakeUserAssignedIdentitiesClient{identity: armmsi.Identity{
		ID:         to.Ptr(testUAMIID),
		Location:   to.Ptr(identity.Spec.Azure.Location),
		Tags:       identityTags,
		Properties: testIdentityProperties(),
	}}
	credential := desiredFederatedIdentityCredential(testIssuerURL, testSubject)
	credential.ID = to.Ptr(testFICID)
	ficClient := &fakeFederatedIdentityCredentialsClient{credential: credential}
	return &fakeIdentityClientSet{
		clients: &identityClients{resourceGroups: rgClient, identities: uamiClient, federatedCredentials: ficClient},
		rg:      rgClient,
		uami:    uamiClient,
		fic:     ficClient,
	}
}

func testIdentityProperties() *armmsi.UserAssignedIdentityProperties {
	return &armmsi.UserAssignedIdentityProperties{
		ClientID:    to.Ptr(testAzureClientID),
		PrincipalID: to.Ptr(testPrincipalID),
		TenantID:    to.Ptr(testTenantID),
	}
}

func recordManagedIdentityStatus(identity *azworkloadidentityv1alpha1.WorkloadIdentity) {
	identity.Status.ClientID = testAzureClientID
	identity.Status.PrincipalID = testPrincipalID
	identity.Status.TenantID = testTenantID
	identity.Status.AzureResources = []azworkloadidentityv1alpha1.AzureResource{
		{ID: testResourceGroupID, Kind: azureResourceKindResourceGroup},
		{ID: testUAMIID, Kind: azureResourceKindUserAssignedIdentity},
		{ID: testFICID, Kind: azureResourceKindFederatedIdentityCredential},
	}
}

func managedTestWorkloadIdentity() *azworkloadidentityv1alpha1.WorkloadIdentity {
	identity := testWorkloadIdentity()
	identity.Spec.Azure = azworkloadidentityv1alpha1.AzureWorkloadIdentityConfig{
		SubscriptionID:                  "test",
		Location:                        "swedencentral",
		ResourceGroupName:               "rg-test",
		UserAssignedIdentityName:        "uami-test",
		FederatedIdentityCredentialName: "fic-test",
	}
	return identity
}

func notFoundResponseError() error {
	return &azcore.ResponseError{StatusCode: http.StatusNotFound}
}

func assertOwnershipConflict(t *testing.T, err error) {
	t.Helper()
	reason, ok := workloadidentity.ConflictReason(err)
	if !ok || reason != workloadidentity.ReasonAzureResourceOwnershipConflict {
		t.Fatalf("expected ownership conflict, got %v", err)
	}
}

func assertDeleteCounts(t *testing.T, set *fakeIdentityClientSet, credentials, identities, resourceGroups int) {
	t.Helper()
	if set.fic.deletes != credentials || set.uami.deletes != identities || set.rg.deletes != resourceGroups {
		t.Fatalf("Azure deletes = credentials:%d identities:%d resourceGroups:%d, want %d/%d/%d", set.fic.deletes, set.uami.deletes, set.rg.deletes, credentials, identities, resourceGroups)
	}
}
