package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
)

func TestIssuerURL(t *testing.T) {
	issuer := testOIDCIssuer()

	if got := issuerURL(issuer); got != "https://oidctest123.blob.core.windows.net/oidc" {
		t.Fatalf("issuerURL = %q", got)
	}
}

func TestResourceTags(t *testing.T) {
	issuer := testOIDCIssuer()
	tags := resourceTags(issuer, true)

	if *tags[managedByTag] != operatorName {
		t.Fatalf("managed-by tag = %q", *tags[managedByTag])
	}
	if *tags["oidc-issuer-uid"] != "test-uid" {
		t.Fatalf("oidc-issuer-uid tag = %q", *tags["oidc-issuer-uid"])
	}
	if *tags[createdByOperatorTag] != operatorCreatedTagValue {
		t.Fatalf("created-by-operator tag = %q", *tags[createdByOperatorTag])
	}
}

func TestMergeTagsPreservesExistingTags(t *testing.T) {
	merged := mergeTags(
		map[string]*string{"environment": to.Ptr("dev"), managedByTag: to.Ptr("someone-else")},
		map[string]*string{managedByTag: to.Ptr(operatorName)},
	)

	if *merged["environment"] != "dev" {
		t.Fatalf("environment tag = %q", *merged["environment"])
	}
	if *merged[managedByTag] != operatorName {
		t.Fatalf("managed-by tag = %q", *merged[managedByTag])
	}
}

func TestWasCreatedByOperator(t *testing.T) {
	issuer := testOIDCIssuer()

	if !wasCreatedByOperator(resourceTags(issuer, true), issuer) {
		t.Fatal("expected created tags to match")
	}
	if wasCreatedByOperator(resourceTags(issuer, false), issuer) {
		t.Fatal("expected adopted tags not to match")
	}
	if wasCreatedByOperator(map[string]*string{managedByTag: to.Ptr("someone-else")}, issuer) {
		t.Fatal("expected unrelated tags not to match")
	}
}

func testOIDCIssuer() *azworkloadidentityv1alpha1.OIDCIssuer {
	return &azworkloadidentityv1alpha1.OIDCIssuer{
		ObjectMeta: metav1.ObjectMeta{Name: azworkloadidentityv1alpha1.OIDCIssuerName, UID: types.UID("test-uid")},
		Spec: azworkloadidentityv1alpha1.OIDCIssuerSpec{
			Azure: azworkloadidentityv1alpha1.AzureOIDCIssuerConfig{
				StorageAccountName: "oidctest123",
				BlobContainerName:  "oidc",
			},
		},
	}
}
