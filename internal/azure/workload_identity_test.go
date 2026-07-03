package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/az-workload-identity-operator/internal/workloadidentity"
)

const testNamespace = "default"

func TestWorkloadIdentityTags(t *testing.T) {
	identity := testWorkloadIdentity()
	tags := workloadIdentityTags(identity, true)

	if *tags[managedByTag] != operatorName {
		t.Fatalf("managed-by tag = %q", *tags[managedByTag])
	}
	if *tags["workload-identity-uid"] != "test-uid" {
		t.Fatalf("workload-identity-uid tag = %q", *tags["workload-identity-uid"])
	}
	if *tags[createdByOperatorTag] != operatorCreatedTagValue {
		t.Fatalf("created-by-operator tag = %q", *tags[createdByOperatorTag])
	}
	if *tags[operatorAPIGroupTag] != operatorAPIGroupValue {
		t.Fatalf("operator-api-group tag = %q", *tags[operatorAPIGroupTag])
	}
}

func TestWasWorkloadIdentityCreatedByOperator(t *testing.T) {
	identity := testWorkloadIdentity()

	if !wasWorkloadIdentityCreatedByOperator(workloadIdentityTags(identity, true), identity) {
		t.Fatal("expected created tags to match")
	}
	if wasWorkloadIdentityCreatedByOperator(workloadIdentityTags(identity, false), identity) {
		t.Fatal("expected adopted tags not to match")
	}
	if wasWorkloadIdentityCreatedByOperator(map[string]*string{managedByTag: to.Ptr("someone-else")}, identity) {
		t.Fatal("expected unrelated tags not to match")
	}
}

func TestIsOperatorResourceOwnedByDifferentWorkloadIdentity(t *testing.T) {
	identity := testWorkloadIdentity()

	if isOperatorResourceOwnedByDifferentWorkloadIdentity(workloadIdentityTags(identity, true), string(identity.UID)) {
		t.Fatal("expected matching owner UID not to conflict")
	}
	if !isOperatorResourceOwnedByDifferentWorkloadIdentity(workloadIdentityTags(identity, true), "other-uid") {
		t.Fatal("expected different owner UID to conflict")
	}
	if isOperatorResourceOwnedByDifferentWorkloadIdentity(map[string]*string{managedByTag: to.Ptr("someone-else")}, string(identity.UID)) {
		t.Fatal("expected unrelated tags not to conflict")
	}
}

func TestValidateFederatedIdentityCredentialAllowsMatchingTuple(t *testing.T) {
	desired := desiredFederatedIdentityCredential("https://issuer.example", "system:serviceaccount:default:test-sa")

	if err := validateFederatedIdentityCredential("fic-test", desired, desired); err != nil {
		t.Fatalf("expected matching credential to be allowed: %v", err)
	}
}

func TestValidateFederatedIdentityCredentialRejectsConflictingTuple(t *testing.T) {
	desired := desiredFederatedIdentityCredential("https://issuer.example", "system:serviceaccount:default:test-sa")
	tests := []struct {
		name     string
		existing armmsi.FederatedIdentityCredential
	}{
		{
			name: "issuer differs",
			existing: armmsi.FederatedIdentityCredential{Properties: &armmsi.FederatedIdentityCredentialProperties{
				Issuer:    to.Ptr("https://other-issuer.example"),
				Subject:   to.Ptr("system:serviceaccount:default:test-sa"),
				Audiences: []*string{to.Ptr(azureADTokenExchangeAudience)},
			}},
		},
		{
			name: "subject differs",
			existing: armmsi.FederatedIdentityCredential{Properties: &armmsi.FederatedIdentityCredentialProperties{
				Issuer:    to.Ptr("https://issuer.example"),
				Subject:   to.Ptr("system:serviceaccount:default:other-sa"),
				Audiences: []*string{to.Ptr(azureADTokenExchangeAudience)},
			}},
		},
		{
			name: "audience differs",
			existing: armmsi.FederatedIdentityCredential{Properties: &armmsi.FederatedIdentityCredentialProperties{
				Issuer:    to.Ptr("https://issuer.example"),
				Subject:   to.Ptr("system:serviceaccount:default:test-sa"),
				Audiences: []*string{to.Ptr("api://other-audience")},
			}},
		},
		{
			name:     "properties missing",
			existing: armmsi.FederatedIdentityCredential{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFederatedIdentityCredential("fic-test", tt.existing, desired)
			reason, ok := workloadidentity.ConflictReason(err)
			if !ok {
				t.Fatalf("expected conflict error, got %v", err)
			}
			if reason != workloadidentity.ReasonFederatedIdentityCredentialConflict {
				t.Fatalf("conflict reason = %q", reason)
			}
		})
	}
}

func TestFederatedIdentityCredentialMatchesStatus(t *testing.T) {
	identity := testWorkloadIdentity()
	identity.Spec.Azure.FederatedIdentityCredentialName = "fic-test"
	identity.Status.IssuerURL = "https://issuer.example"
	identity.Status.Subject = "system:serviceaccount:default:test-sa"
	matching := desiredFederatedIdentityCredential(identity.Status.IssuerURL, identity.Status.Subject)
	conflicting := desiredFederatedIdentityCredential(identity.Status.IssuerURL, "system:serviceaccount:default:other-sa")

	if !federatedIdentityCredentialMatchesStatus(identity, matching) {
		t.Fatal("expected credential matching status to be owned")
	}
	if federatedIdentityCredentialMatchesStatus(identity, conflicting) {
		t.Fatal("expected credential with different subject not to match status")
	}

	identity.Status.Subject = ""
	if federatedIdentityCredentialMatchesStatus(identity, matching) {
		t.Fatal("expected credential not to match empty status")
	}
}

func testWorkloadIdentity() *azworkloadidentityv1alpha1.WorkloadIdentity {
	return &azworkloadidentityv1alpha1.WorkloadIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: azworkloadidentityv1alpha1.OIDCIssuerName, Namespace: testNamespace, UID: types.UID("test-uid")},
	}
}
