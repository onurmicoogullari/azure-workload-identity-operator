package oidcissuer

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
)

const guardTestIssuerURL = "https://oidctest123.blob.core.windows.net/oidc"

type fakeWorkloadIdentityLister struct {
	items []azworkloadidentityv1alpha1.WorkloadIdentity
	err   error
}

type fakeServiceAccountTokenIssuer struct {
	currentIssuer string
	err           error
	gets          int
}

type fakeOpenShiftServiceAccountIssuer struct {
	currentIssuer string
	err           error
	gets          int
}

func (f *fakeWorkloadIdentityLister) List(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
	if f.err != nil {
		return f.err
	}
	identities := list.(*azworkloadidentityv1alpha1.WorkloadIdentityList)
	identities.Items = append(identities.Items, f.items...)
	return nil
}

func (f *fakeServiceAccountTokenIssuer) CurrentIssuer(context.Context) (string, error) {
	f.gets++
	return f.currentIssuer, f.err
}

func (f *fakeOpenShiftServiceAccountIssuer) Get(context.Context) (string, error) {
	f.gets++
	return f.currentIssuer, f.err
}

func TestCheckWorkloadIdentityDeletionBlock(t *testing.T) {
	result, err := CheckWorkloadIdentityDeletionBlock(context.Background(), &fakeWorkloadIdentityLister{
		items: []azworkloadidentityv1alpha1.WorkloadIdentity{{
			ObjectMeta: metav1.ObjectMeta{Name: "blocking-workload", Namespace: "default"},
		}},
	}, 5)

	if err != nil {
		t.Fatalf("CheckWorkloadIdentityDeletionBlock returned error: %v", err)
	}
	if !result.Blocked {
		t.Fatal("expected deletion to be blocked")
	}
	if result.Reason != ReasonBlockedByWorkloadIdentities {
		t.Fatalf("reason = %q", result.Reason)
	}
	if result.WorkloadIdentityCount != 1 {
		t.Fatalf("WorkloadIdentityCount = %d", result.WorkloadIdentityCount)
	}
	if !strings.Contains(result.Message, "default/blocking-workload") {
		t.Fatalf("message = %q", result.Message)
	}
}

func TestCheckClusterServiceAccountIssuerHandoffWithoutIssuerURL(t *testing.T) {
	tokens := &fakeServiceAccountTokenIssuer{currentIssuer: guardTestIssuerURL}
	result, err := CheckClusterServiceAccountIssuerHandoff(context.Background(), guardTestIssuer(""), tokens, nil)

	if err != nil {
		t.Fatalf("CheckClusterServiceAccountIssuerHandoff returned error: %v", err)
	}
	if result.Blocked || result.CheckFailed {
		t.Fatalf("expected empty result, got %#v", result)
	}
	if tokens.gets != 0 {
		t.Fatalf("CurrentIssuer calls = %d", tokens.gets)
	}
}

func TestCheckClusterServiceAccountIssuerHandoffGuardUnavailable(t *testing.T) {
	result, err := CheckClusterServiceAccountIssuerHandoff(context.Background(), guardTestIssuer(guardTestIssuerURL), nil, nil)

	if err != nil {
		t.Fatalf("CheckClusterServiceAccountIssuerHandoff returned error: %v", err)
	}
	if !result.Blocked {
		t.Fatal("expected deletion to be blocked")
	}
	if result.Reason != ReasonClusterServiceAccountIssuerGuardUnavailable {
		t.Fatalf("reason = %q", result.Reason)
	}
	if !strings.Contains(result.Message, "cannot verify") {
		t.Fatalf("message = %q", result.Message)
	}
}

func TestCheckClusterServiceAccountIssuerHandoffSkipsTokenGuardWhenOpenShiftReaderExists(t *testing.T) {
	result, err := CheckClusterServiceAccountIssuerHandoff(
		context.Background(),
		guardTestIssuer(guardTestIssuerURL),
		nil,
		&fakeOpenShiftServiceAccountIssuer{currentIssuer: guardTestIssuerURL},
	)

	if err != nil {
		t.Fatalf("CheckClusterServiceAccountIssuerHandoff returned error: %v", err)
	}
	if result.Blocked || result.CheckFailed {
		t.Fatalf("expected empty result, got %#v", result)
	}
}

func TestCheckClusterServiceAccountIssuerHandoffBlocksWhenTokenIssuerMatches(t *testing.T) {
	tokens := &fakeServiceAccountTokenIssuer{currentIssuer: guardTestIssuerURL}
	result, err := CheckClusterServiceAccountIssuerHandoff(context.Background(), guardTestIssuer(guardTestIssuerURL), tokens, nil)

	if err != nil {
		t.Fatalf("CheckClusterServiceAccountIssuerHandoff returned error: %v", err)
	}
	if !result.Blocked {
		t.Fatal("expected deletion to be blocked")
	}
	if result.Reason != ReasonBlockedByClusterServiceAccountIssuer {
		t.Fatalf("reason = %q", result.Reason)
	}
	if tokens.gets != 1 {
		t.Fatalf("CurrentIssuer calls = %d", tokens.gets)
	}
}

func TestCheckClusterServiceAccountIssuerHandoffAllowsTokenIssuerHandoff(t *testing.T) {
	tokens := &fakeServiceAccountTokenIssuer{currentIssuer: "https://issuer.example"}
	result, err := CheckClusterServiceAccountIssuerHandoff(context.Background(), guardTestIssuer(guardTestIssuerURL), tokens, nil)

	if err != nil {
		t.Fatalf("CheckClusterServiceAccountIssuerHandoff returned error: %v", err)
	}
	if result.Blocked || result.CheckFailed {
		t.Fatalf("expected empty result, got %#v", result)
	}
	if tokens.gets != 1 {
		t.Fatalf("CurrentIssuer calls = %d", tokens.gets)
	}
}

func TestCheckClusterServiceAccountIssuerHandoffReportsTokenReadError(t *testing.T) {
	readErr := errors.New("token request forbidden")
	result, err := CheckClusterServiceAccountIssuerHandoff(
		context.Background(),
		guardTestIssuer(guardTestIssuerURL),
		&fakeServiceAccountTokenIssuer{err: readErr},
		nil,
	)

	if err != nil {
		t.Fatalf("CheckClusterServiceAccountIssuerHandoff returned error: %v", err)
	}
	if !result.CheckFailed {
		t.Fatal("expected check failure")
	}
	if result.Reason != ReasonClusterServiceAccountIssuerCheckFailed {
		t.Fatalf("reason = %q", result.Reason)
	}
	if !errors.Is(result.Err, readErr) {
		t.Fatalf("Err = %v", result.Err)
	}
	if !strings.Contains(result.Message, "token request forbidden") {
		t.Fatalf("message = %q", result.Message)
	}
}

func TestCheckOpenShiftServiceAccountIssuerHandoffBlocksWhenIssuerMatches(t *testing.T) {
	openShift := &fakeOpenShiftServiceAccountIssuer{currentIssuer: guardTestIssuerURL}
	result, err := CheckOpenShiftServiceAccountIssuerHandoff(context.Background(), guardTestIssuer(guardTestIssuerURL), openShift)

	if err != nil {
		t.Fatalf("CheckOpenShiftServiceAccountIssuerHandoff returned error: %v", err)
	}
	if !result.Blocked {
		t.Fatal("expected deletion to be blocked")
	}
	if result.Reason != ReasonBlockedByOpenShiftServiceAccountIssuer {
		t.Fatalf("reason = %q", result.Reason)
	}
	if openShift.gets != 1 {
		t.Fatalf("Get calls = %d", openShift.gets)
	}
}

func TestCheckOpenShiftServiceAccountIssuerHandoffAllowsIssuerHandoff(t *testing.T) {
	openShift := &fakeOpenShiftServiceAccountIssuer{currentIssuer: "https://issuer.example"}
	result, err := CheckOpenShiftServiceAccountIssuerHandoff(context.Background(), guardTestIssuer(guardTestIssuerURL), openShift)

	if err != nil {
		t.Fatalf("CheckOpenShiftServiceAccountIssuerHandoff returned error: %v", err)
	}
	if result.Blocked || result.CheckFailed {
		t.Fatalf("expected empty result, got %#v", result)
	}
	if openShift.gets != 1 {
		t.Fatalf("Get calls = %d", openShift.gets)
	}
}

func guardTestIssuer(issuerURL string) *azworkloadidentityv1alpha1.OIDCIssuer {
	return &azworkloadidentityv1alpha1.OIDCIssuer{
		ObjectMeta: metav1.ObjectMeta{Name: azworkloadidentityv1alpha1.OIDCIssuerName},
		Status: azworkloadidentityv1alpha1.OIDCIssuerStatus{
			IssuerURL: issuerURL,
		},
	}
}
