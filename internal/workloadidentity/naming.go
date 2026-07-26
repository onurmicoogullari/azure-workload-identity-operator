package workloadidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
)

const maxUserAssignedIdentityNameLength = 128

var userAssignedIdentityNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// UserAssignedIdentityName returns the platform-scoped Azure identity name.
func UserAssignedIdentityName(namespace, suffix string) string {
	return namespace + "-" + suffix
}

// LogicalIdentityKey identifies a Kubernetes WorkloadIdentity independent of
// its object UID so a delete-and-recreate requires an explicit recovery flow.
func LogicalIdentityKey(namespace, name string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + name))
	return hex.EncodeToString(sum[:])
}

func ValidateUserAssignedIdentityName(name string) error {
	if len(name) < 3 || len(name) > maxUserAssignedIdentityNameLength {
		return fmt.Errorf("resolved user assigned identity name must contain between 3 and %d characters", maxUserAssignedIdentityNameLength)
	}
	if !userAssignedIdentityNamePattern.MatchString(name) {
		return fmt.Errorf("resolved user assigned identity name must contain only alphanumeric characters, hyphens, and underscores and must start with an alphanumeric character")
	}
	return nil
}
