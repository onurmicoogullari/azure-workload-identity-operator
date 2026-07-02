package oidcissuer

import (
	"fmt"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
)

func HasPublishedIssuerURL(issuer *azworkloadidentityv1alpha1.OIDCIssuer) bool {
	return issuer.Status.IssuerURL != ""
}

func ClusterServiceAccountIssuerDeletionBlockedMessage(issuerURL string) string {
	return fmt.Sprintf(
		"OIDCIssuer deletion is blocked because the cluster is still minting service account tokens with issuer %q. Change the cluster service account issuer before deleting this OIDCIssuer.",
		issuerURL,
	)
}

func ClusterServiceAccountIssuerCheckFailedMessage(issuerURL string, err error) string {
	return fmt.Sprintf(
		"OIDCIssuer deletion is blocked because the operator could not verify whether the cluster still mints service account tokens with issuer %q: %v",
		issuerURL,
		err,
	)
}

func ClusterServiceAccountIssuerGuardUnavailableMessage(issuerURL string) string {
	return fmt.Sprintf(
		"OIDCIssuer deletion is blocked because the operator cannot verify whether the cluster still mints service account tokens with issuer %q. Configure the service account token issuer deletion guard before deleting this OIDCIssuer.",
		issuerURL,
	)
}

// OpenShiftServiceAccountIssuerDeletionBlockedMessage explains the manual handoff required before deletion.
func OpenShiftServiceAccountIssuerDeletionBlockedMessage(issuerURL string) string {
	return fmt.Sprintf(
		"OIDCIssuer deletion is blocked because OpenShift Authentication/cluster.spec.serviceAccountIssuer still references %q. Restore or change the OpenShift service account issuer before deleting this OIDCIssuer.",
		issuerURL,
	)
}
