package workloadidentity

import (
	"fmt"
	"slices"
	"strings"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
)

// ResourceReferences returns stable namespace/name references for WorkloadIdentity resources.
func ResourceReferences(identities []azworkloadidentityv1alpha1.WorkloadIdentity, limit int) []string {
	references := make([]string, 0, len(identities))
	for _, identity := range identities {
		references = append(references, fmt.Sprintf("%s/%s", identity.Namespace, identity.Name))
	}
	slices.Sort(references)
	if limit <= 0 || len(references) <= limit {
		return references
	}

	remaining := len(references) - limit
	return append(references[:limit], fmt.Sprintf("and %d more", remaining))
}

// DeletionBlockedMessage explains why OIDCIssuer deletion is blocked.
func DeletionBlockedMessage(identities []azworkloadidentityv1alpha1.WorkloadIdentity, limit int) string {
	return fmt.Sprintf(
		"OIDCIssuer deletion is blocked by %d WorkloadIdentity resource(s): %s",
		len(identities),
		strings.Join(ResourceReferences(identities, limit), ", "),
	)
}
