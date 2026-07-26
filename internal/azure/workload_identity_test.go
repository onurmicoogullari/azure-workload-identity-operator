package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"

	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

func TestWorkloadIdentityTagsContainCompleteImmutableOwnership(t *testing.T) {
	identity := managedTestWorkloadIdentity()
	tags := workloadIdentityTags(identity)

	expected := map[string]string{
		managedByTag:           operatorName,
		operatorAPIGroupTag:    operatorAPIGroupValue,
		createdByOperatorTag:   operatorCreatedTagValue,
		workloadIdentityUIDTag: string(identity.UID),
		workloadIdentityKeyTag: workloadidentity.LogicalIdentityKey(identity.Namespace, identity.Name),
	}
	for key, want := range expected {
		if got := stringValue(tags[key]); got != want {
			t.Fatalf("tag %q = %q, want %q", key, got, want)
		}
	}
	if _, exists := tags["federated-credential-name"]; exists {
		t.Fatal("obsolete federated credential name tag was set")
	}
}

func TestValidateUserAssignedIdentityOwnershipDistinguishesRecovery(t *testing.T) {
	identity := managedTestWorkloadIdentity()

	recreatedTags := withTag(workloadIdentityTags(identity), workloadIdentityUIDTag, "old-uid")
	err := validateUserAssignedIdentityOwnership(identity, armmsi.Identity{
		ID:   to.Ptr(testUAMIID),
		Tags: recreatedTags,
	})
	assertConflictReason(t, err, workloadidentity.ReasonRecoveryRequired)

	foreignTags := withTag(workloadIdentityTags(identity), workloadIdentityKeyTag, "foreign")
	err = validateUserAssignedIdentityOwnership(identity, armmsi.Identity{
		ID:   to.Ptr(testUAMIID),
		Tags: foreignTags,
	})
	assertConflictReason(t, err, workloadidentity.ReasonAzureResourceOwnershipConflict)
}

func TestValidateFederatedIdentityCredentialRequiresExactTuple(t *testing.T) {
	desired := desiredFederatedIdentityCredential(testIssuerURL, testSubject)
	if err := validateFederatedIdentityCredential("fic-test", desired, desired); err != nil {
		t.Fatalf("matching credential: %v", err)
	}

	tests := []struct {
		name     string
		existing armmsi.FederatedIdentityCredential
	}{
		{
			name: "issuer differs",
			existing: armmsi.FederatedIdentityCredential{Properties: &armmsi.FederatedIdentityCredentialProperties{
				Issuer:    to.Ptr("https://other.example"),
				Subject:   to.Ptr(testSubject),
				Audiences: []*string{to.Ptr(azureADTokenExchangeAudience)},
			}},
		},
		{
			name: "subject differs",
			existing: armmsi.FederatedIdentityCredential{Properties: &armmsi.FederatedIdentityCredentialProperties{
				Issuer:    to.Ptr(testIssuerURL),
				Subject:   to.Ptr("system:serviceaccount:default:other"),
				Audiences: []*string{to.Ptr(azureADTokenExchangeAudience)},
			}},
		},
		{
			name: "audience differs",
			existing: armmsi.FederatedIdentityCredential{Properties: &armmsi.FederatedIdentityCredentialProperties{
				Issuer:    to.Ptr(testIssuerURL),
				Subject:   to.Ptr(testSubject),
				Audiences: []*string{to.Ptr("api://other")},
			}},
		},
		{
			name: "extra audience",
			existing: armmsi.FederatedIdentityCredential{Properties: &armmsi.FederatedIdentityCredentialProperties{
				Issuer:    to.Ptr(testIssuerURL),
				Subject:   to.Ptr(testSubject),
				Audiences: []*string{to.Ptr(azureADTokenExchangeAudience), to.Ptr("api://other")},
			}},
		},
		{name: "properties missing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFederatedIdentityCredential("fic-test", tt.existing, desired)
			assertConflictReason(t, err, workloadidentity.ReasonFederatedIdentityCredentialConflict)
		})
	}
}
