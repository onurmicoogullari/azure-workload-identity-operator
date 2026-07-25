package azure

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

const (
	testSubscriptionID  = "00000000-0000-0000-0000-000000000000"
	testResourceGroupID = "/subscriptions/" + testSubscriptionID + "/resourceGroups/rg-test"
	testUAMIName        = "default-uami-test"
	testUAMIID          = testResourceGroupID + "/providers/Microsoft.ManagedIdentity/userAssignedIdentities/" + testUAMIName
	testFICID           = testUAMIID + "/federatedIdentityCredentials/fic-test"
	testAzureClientID   = "11111111-1111-1111-1111-111111111111"
	testPrincipalID     = "22222222-2222-2222-2222-222222222222"
	testTenantID        = "33333333-3333-3333-3333-333333333333"
	testIssuerURL       = "https://issuer.example"
	testSubject         = "system:serviceaccount:default:test-sa"
)

var testScope = mustTestScope()

type fakeResourceGroupsClient struct {
	resourceGroup armresources.ResourceGroup
	getErr        error
	gets          int
	puts          int
	lastPut       armresources.ResourceGroup
}

func (f *fakeResourceGroupsClient) Get(
	context.Context,
	string,
	*armresources.ResourceGroupsClientGetOptions,
) (armresources.ResourceGroupsClientGetResponse, error) {
	f.gets++
	return armresources.ResourceGroupsClientGetResponse{ResourceGroup: f.resourceGroup}, f.getErr
}

func (f *fakeResourceGroupsClient) CreateOrUpdate(
	_ context.Context,
	_ string,
	resourceGroup armresources.ResourceGroup,
	_ *armresources.ResourceGroupsClientCreateOrUpdateOptions,
) (armresources.ResourceGroupsClientCreateOrUpdateResponse, error) {
	f.puts++
	f.lastPut = resourceGroup
	if resourceGroup.ID == nil {
		resourceGroup.ID = f.resourceGroup.ID
	}
	f.resourceGroup = resourceGroup
	return armresources.ResourceGroupsClientCreateOrUpdateResponse{ResourceGroup: resourceGroup}, nil
}

type fakeUserAssignedIdentitiesClient struct {
	identity      armmsi.Identity
	getIdentities []armmsi.Identity
	getErr        error
	getErrors     []error
	createErr     error
	gets          int
	putAttempts   int
	puts          int
	deletes       int
	lastName      string
	lastPut       armmsi.Identity
	events        *[]string
}

func (f *fakeUserAssignedIdentitiesClient) Get(
	_ context.Context,
	_ string,
	name string,
	_ *armmsi.UserAssignedIdentitiesClientGetOptions,
) (armmsi.UserAssignedIdentitiesClientGetResponse, error) {
	index := f.gets
	f.gets++
	f.lastName = name
	identity := f.identity
	if index < len(f.getIdentities) {
		identity = f.getIdentities[index]
	}
	err := f.getErr
	if index < len(f.getErrors) {
		err = f.getErrors[index]
	}
	return armmsi.UserAssignedIdentitiesClientGetResponse{Identity: identity}, err
}

func (f *fakeUserAssignedIdentitiesClient) CreateOrUpdate(
	_ context.Context,
	_, name string,
	identity armmsi.Identity,
	_ *armmsi.UserAssignedIdentitiesClientCreateOrUpdateOptions,
) (armmsi.UserAssignedIdentitiesClientCreateOrUpdateResponse, error) {
	f.putAttempts++
	f.lastName = name
	f.lastPut = identity
	if f.createErr != nil {
		return armmsi.UserAssignedIdentitiesClientCreateOrUpdateResponse{}, f.createErr
	}
	f.puts++
	identity.ID = to.Ptr(testUAMIID)
	identity.Properties = testIdentityProperties()
	f.identity = identity
	if f.events != nil {
		*f.events = append(*f.events, "uami-create")
	}
	return armmsi.UserAssignedIdentitiesClientCreateOrUpdateResponse{Identity: identity}, nil
}

func (f *fakeUserAssignedIdentitiesClient) Delete(
	context.Context,
	string,
	string,
	*armmsi.UserAssignedIdentitiesClientDeleteOptions,
) (armmsi.UserAssignedIdentitiesClientDeleteResponse, error) {
	f.deletes++
	if f.events != nil {
		*f.events = append(*f.events, "uami-delete")
	}
	return armmsi.UserAssignedIdentitiesClientDeleteResponse{}, nil
}

type fakeFederatedIdentityCredentialsClient struct {
	credential armmsi.FederatedIdentityCredential
	getErr     error
	gets       int
	puts       int
	events     *[]string
}

func (f *fakeFederatedIdentityCredentialsClient) Get(
	context.Context,
	string,
	string,
	string,
	*armmsi.FederatedIdentityCredentialsClientGetOptions,
) (armmsi.FederatedIdentityCredentialsClientGetResponse, error) {
	f.gets++
	return armmsi.FederatedIdentityCredentialsClientGetResponse{
		FederatedIdentityCredential: f.credential,
	}, f.getErr
}

func (f *fakeFederatedIdentityCredentialsClient) CreateOrUpdate(
	_ context.Context,
	_, _, _ string,
	credential armmsi.FederatedIdentityCredential,
	_ *armmsi.FederatedIdentityCredentialsClientCreateOrUpdateOptions,
) (armmsi.FederatedIdentityCredentialsClientCreateOrUpdateResponse, error) {
	f.puts++
	if f.events != nil {
		*f.events = append(*f.events, "fic-create-or-update")
	}
	credential.ID = to.Ptr(testFICID)
	f.credential = credential
	return armmsi.FederatedIdentityCredentialsClientCreateOrUpdateResponse{
		FederatedIdentityCredential: credential,
	}, nil
}

type fakeIdentityClientSet struct {
	clients *identityClients
	rg      *fakeResourceGroupsClient
	uami    *fakeUserAssignedIdentitiesClient
	fic     *fakeFederatedIdentityCredentialsClient
}

func TestSharedResourceGroupIsAcceptedWithoutMutation(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	set := testClientSet(workloadIdentityTags(identity))
	set.rg.resourceGroup.Tags = map[string]*string{"platform-owner": to.Ptr("team-a")}

	if _, _, err := set.clients.ensureUserAssignedIdentity(context.Background(), identity); err != nil {
		t.Fatalf("ensure user assigned identity: %v", err)
	}
	if set.rg.puts != 0 {
		t.Fatalf("resource group writes = %d, want 0", set.rg.puts)
	}
	if got := stringValue(set.rg.resourceGroup.Tags["platform-owner"]); got != "team-a" {
		t.Fatalf("platform ownership tag = %q", got)
	}
}

func TestSharedResourceGroupIsCreatedWithoutWorkloadOwnershipTags(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	set := testClientSet(workloadIdentityTags(identity))
	set.rg.getErr = notFoundResponseError()

	if _, _, err := set.clients.ensureUserAssignedIdentity(context.Background(), identity); err != nil {
		t.Fatalf("ensure user assigned identity: %v", err)
	}
	if set.rg.puts != 1 {
		t.Fatalf("resource group writes = %d, want 1", set.rg.puts)
	}
	if set.rg.lastPut.Tags != nil {
		t.Fatalf("shared resource group received ownership tags: %v", set.rg.lastPut.Tags)
	}
	if got := stringValue(set.rg.lastPut.Location); got != testScope.location {
		t.Fatalf("resource group location = %q", got)
	}
}

func TestNewUserAssignedIdentityUsesResolvedNameAndCompleteOwnershipTags(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	set := testClientSet(nil)
	set.uami.getErr = notFoundResponseError()

	_, created, err := set.clients.ensureUserAssignedIdentity(context.Background(), identity)
	if err != nil {
		t.Fatalf("create user assigned identity: %v", err)
	}
	if set.uami.puts != 1 || set.uami.lastName != testUAMIName {
		t.Fatalf("identity create = %d name %q, want 1 and %q", set.uami.puts, set.uami.lastName, testUAMIName)
	}
	if err := validateUserAssignedIdentityOwnership(identity, created); err != nil {
		t.Fatalf("created identity ownership: %v", err)
	}
	if stringValue(created.Tags[workloadIdentityKeyTag]) != workloadidentity.LogicalIdentityKey(identity.Namespace, identity.Name) {
		t.Fatal("logical workload identity key tag was not set")
	}
	if _, exists := created.Tags["federated-credential-name"]; exists {
		t.Fatal("obsolete federated credential name tag was set")
	}
}

func TestInvalidResolvedUserAssignedIdentityNameCausesNoAzureCalls(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	identity.Namespace = strings.Repeat("n", 63)
	identity.Spec.Azure.UserAssignedIdentityName = strings.Repeat("a", 66)
	set := testClientSet(nil)

	_, err := set.clients.ensure(context.Background(), identity, testIssuerURL, testSubject)

	if err == nil || !strings.Contains(err.Error(), "resolved user assigned identity name") {
		t.Fatalf("expected resolved identity name validation error, got %v", err)
	}
	if set.rg.gets != 0 || set.rg.puts != 0 || set.uami.gets != 0 {
		t.Fatalf(
			"unexpected Azure reads/writes: resource group gets=%d puts=%d; UAMI gets=%d",
			set.rg.gets,
			set.rg.puts,
			set.uami.gets,
		)
	}
	assertNoIdentityOrCredentialWrites(t, set)
}

func TestInvalidResolvedUserAssignedIdentityNameDoesNotBlockDeletion(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	identity.Namespace = strings.Repeat("n", 63)
	identity.Spec.Azure.UserAssignedIdentityName = strings.Repeat("a", 66)
	set := testClientSet(nil)

	if err := set.clients.delete(context.Background(), identity); err != nil {
		t.Fatalf("delete invalid workload identity: %v", err)
	}
	if set.rg.gets != 0 || set.rg.puts != 0 || set.uami.gets != 0 {
		t.Fatalf(
			"unexpected Azure reads/writes: resource group gets=%d puts=%d; UAMI gets=%d",
			set.rg.gets,
			set.rg.puts,
			set.uami.gets,
		)
	}
	assertNoIdentityOrCredentialWrites(t, set)
}

func TestForeignOrMismatchedUserAssignedIdentityNeverCausesUAMIOrFICWrites(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	validTags := workloadIdentityTags(identity)
	tests := []struct {
		name string
		tags map[string]*string
	}{
		{name: "untagged foreign identity", tags: nil},
		{name: "foreign manager", tags: withTag(validTags, managedByTag, "someone-else")},
		{name: "not operator created", tags: withTag(validTags, createdByOperatorTag, "false")},
		{name: "different logical key", tags: withTag(validTags, workloadIdentityKeyTag, "different-key")},
		{name: "different UID without matching key", tags: withTags(validTags, map[string]string{
			workloadIdentityKeyTag: "different-key",
			workloadIdentityUIDTag: "different-uid",
		})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := testClientSet(tt.tags)

			_, err := set.clients.ensure(context.Background(), identity, testIssuerURL, testSubject)
			assertConflictReason(t, err, workloadidentity.ReasonAzureResourceOwnershipConflict)
			assertNoIdentityOrCredentialWrites(t, set)
		})
	}
}

func TestEarlierUIDRequiresRecoveryWithoutUAMIOrFICWrites(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	tags := withTag(workloadIdentityTags(identity), workloadIdentityUIDTag, "earlier-uid")
	set := testClientSet(tags)

	_, err := set.clients.ensure(context.Background(), identity, testIssuerURL, testSubject)
	assertConflictReason(t, err, workloadidentity.ReasonRecoveryRequired)
	assertNoIdentityOrCredentialWrites(t, set)
}

func TestSuccessfulReconciliationReadsUserAssignedIdentityOnce(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	set := testClientSet(workloadIdentityTags(identity))

	if _, err := set.clients.ensure(context.Background(), identity, testIssuerURL, testSubject); err != nil {
		t.Fatalf("ensure workload identity: %v", err)
	}
	if set.uami.gets != 1 {
		t.Fatalf("user assigned identity reads = %d, want 1", set.uami.gets)
	}
	if set.fic.gets != 1 {
		t.Fatalf("federated identity credential reads = %d, want 1", set.fic.gets)
	}
	if set.uami.puts != 0 || set.fic.puts != 0 {
		t.Fatalf("unexpected Azure writes: UAMI=%d FIC=%d", set.uami.puts, set.fic.puts)
	}
}

func TestOwnedFederatedCredentialDriftIsRepairedAndVerified(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	set := testClientSet(workloadIdentityTags(identity))
	set.fic.credential = desiredFederatedIdentityCredential("https://old.example", testSubject)

	credential, err := set.clients.ensureFederatedIdentityCredential(
		context.Background(),
		identity,
		testIssuerURL,
		testSubject,
	)
	if err != nil {
		t.Fatalf("repair federated identity credential: %v", err)
	}
	if set.fic.puts != 1 {
		t.Fatalf("credential writes = %d, want 1", set.fic.puts)
	}
	if err := validateFederatedIdentityCredential(
		identity.Spec.Azure.FederatedIdentityCredentialName,
		credential,
		desiredFederatedIdentityCredential(testIssuerURL, testSubject),
	); err != nil {
		t.Fatalf("repaired credential validation: %v", err)
	}
}

func TestDeleteVerifiesOwnershipAndReliesOnParentCascade(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	set := testClientSet(workloadIdentityTags(identity))
	var events []string
	set.uami.events = &events

	if err := set.clients.delete(context.Background(), identity); err != nil {
		t.Fatalf("delete owned resources: %v", err)
	}
	if set.uami.deletes != 1 {
		t.Fatalf("identity deletes = %d, want 1", set.uami.deletes)
	}
	if set.rg.gets != 0 || set.rg.puts != 0 {
		t.Fatalf("shared resource group was accessed during deletion: gets=%d puts=%d", set.rg.gets, set.rg.puts)
	}
	if len(events) != 1 || events[0] != "uami-delete" {
		t.Fatalf("delete events = %v, want [uami-delete]", events)
	}
}

func TestDeleteOwnershipMismatchCausesNoAzureWrites(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	validTags := workloadIdentityTags(identity)
	tests := []struct {
		name string
		tags map[string]*string
	}{
		{name: "untagged foreign identity", tags: nil},
		{name: "foreign manager", tags: withTag(validTags, managedByTag, "someone-else")},
		{name: "not operator created", tags: withTag(validTags, createdByOperatorTag, "false")},
		{name: "different logical key", tags: withTag(validTags, workloadIdentityKeyTag, "different-key")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := testClientSet(tt.tags)

			err := set.clients.delete(context.Background(), identity)
			assertConflictReason(t, err, workloadidentity.ReasonAzureResourceOwnershipConflict)
			assertNoIdentityOrCredentialWrites(t, set)
		})
	}
}

func TestDeletePreservesEarlierUIDResourcesForRecovery(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	set := testClientSet(withTag(workloadIdentityTags(identity), workloadIdentityUIDTag, "earlier-uid"))

	err := set.clients.delete(context.Background(), identity)
	assertConflictReason(t, err, workloadidentity.ReasonRecoveryRequired)
	if set.uami.deletes != 0 {
		t.Fatalf("identity deletes = %d, want 0", set.uami.deletes)
	}
}

func TestRecordedMissingIdentityIsNotRecreated(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	identity.Status.AzureResources = []azworkloadidentityv1alpha1.AzureResource{{
		ID:   testUAMIID,
		Kind: azureResourceKindUserAssignedIdentity,
	}}
	set := testClientSet(workloadIdentityTags(identity))
	set.uami.getErr = notFoundResponseError()

	_, _, err := set.clients.ensureUserAssignedIdentity(context.Background(), identity)
	assertConflictReason(t, err, workloadidentity.ReasonAzureResourceOwnershipConflict)
	if set.uami.puts != 0 {
		t.Fatalf("identity creates = %d, want 0", set.uami.puts)
	}
}

func testClientSet(identityTags map[string]*string) *fakeIdentityClientSet {
	rgClient := &fakeResourceGroupsClient{resourceGroup: armresources.ResourceGroup{
		ID:       to.Ptr(testResourceGroupID),
		Location: to.Ptr(testScope.location),
	}}
	uamiClient := &fakeUserAssignedIdentitiesClient{identity: armmsi.Identity{
		ID:         to.Ptr(testUAMIID),
		Location:   to.Ptr(testScope.location),
		Tags:       identityTags,
		Properties: testIdentityProperties(),
	}}
	credential := desiredFederatedIdentityCredential(testIssuerURL, testSubject)
	credential.ID = to.Ptr(testFICID)
	ficClient := &fakeFederatedIdentityCredentialsClient{credential: credential}
	return &fakeIdentityClientSet{
		clients: &identityClients{
			scope:                testScope,
			resourceGroups:       rgClient,
			identities:           uamiClient,
			federatedCredentials: ficClient,
		},
		rg:   rgClient,
		uami: uamiClient,
		fic:  ficClient,
	}
}

func managedTestWorkloadIdentity() *azworkloadidentityv1alpha1.WorkloadIdentity {
	return &azworkloadidentityv1alpha1.WorkloadIdentity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-workload",
			Namespace: "default",
			UID:       types.UID("test-uid"),
		},
		Spec: azworkloadidentityv1alpha1.WorkloadIdentitySpec{
			Azure: azworkloadidentityv1alpha1.AzureWorkloadIdentityConfig{
				UserAssignedIdentityName:        "uami-test",
				FederatedIdentityCredentialName: "fic-test",
			},
			ServiceAccount: azworkloadidentityv1alpha1.ServiceAccountReference{Name: "test-sa"},
		},
	}
}

func testIdentityProperties() *armmsi.UserAssignedIdentityProperties {
	return &armmsi.UserAssignedIdentityProperties{
		ClientID:    to.Ptr(testAzureClientID),
		PrincipalID: to.Ptr(testPrincipalID),
		TenantID:    to.Ptr(testTenantID),
	}
}

func mustTestScope() Scope {
	scope, err := NewScope(testSubscriptionID, "rg-test", "swedencentral")
	if err != nil {
		panic(err)
	}
	return scope
}

func notFoundResponseError() error {
	return &azcore.ResponseError{StatusCode: http.StatusNotFound}
}

func withTag(tags map[string]*string, key, value string) map[string]*string {
	return withTags(tags, map[string]string{key: value})
}

func withTags(tags map[string]*string, changes map[string]string) map[string]*string {
	result := mergeTags(tags, nil)
	for key, value := range changes {
		result[key] = to.Ptr(value)
	}
	return result
}

func assertConflictReason(t *testing.T, err error, want string) {
	t.Helper()
	reason, ok := workloadidentity.ConflictReason(err)
	if !ok || reason != want {
		t.Fatalf("conflict reason = %q, %t, want %q; error: %v", reason, ok, want, err)
	}
}

func assertNoIdentityOrCredentialWrites(t *testing.T, set *fakeIdentityClientSet) {
	t.Helper()
	if set.uami.puts != 0 || set.uami.deletes != 0 || set.fic.puts != 0 {
		t.Fatalf(
			"unexpected UAMI/FIC writes: uami puts=%d deletes=%d; fic puts=%d",
			set.uami.puts,
			set.uami.deletes,
			set.fic.puts,
		)
	}
	if set.fic.gets != 0 {
		t.Fatalf("federated credential reads = %d, want 0 before ownership verification", set.fic.gets)
	}
}
