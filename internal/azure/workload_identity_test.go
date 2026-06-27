package azure

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
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

func testWorkloadIdentity() *azworkloadidentityv1alpha1.WorkloadIdentity {
	return &azworkloadidentityv1alpha1.WorkloadIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: azworkloadidentityv1alpha1.OIDCIssuerName, Namespace: testNamespace, UID: types.UID("test-uid")},
	}
}
