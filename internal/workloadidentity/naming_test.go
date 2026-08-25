package workloadidentity

import (
	"strings"
	"testing"
)

func TestLogicalIdentityKeyIsLowercaseSHA256OfNamespacedName(t *testing.T) {
	const want = "cbd46f5e2909558fcadde645d96e145f96643cc303587dd6ddc6325a34ed8d2f"
	if got := LogicalIdentityKey("default", "test-workload"); got != want {
		t.Fatalf("logical identity key = %q, want %q", got, want)
	}
}

func TestUserAssignedIdentityNameValidation(t *testing.T) {
	if err := ValidateUserAssignedIdentityName("payments"); err != nil {
		t.Fatalf("valid name: %v", err)
	}
	if err := ValidateUserAssignedIdentityName(strings.Repeat("a", 129)); err == nil {
		t.Fatal("expected overlength Azure identity name to be rejected")
	}
	if err := ValidateUserAssignedIdentityName("team-a.invalid"); err == nil {
		t.Fatal("expected invalid Azure identity syntax to be rejected")
	}
}
