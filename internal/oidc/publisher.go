package oidc

import (
	"context"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
)

type Publisher interface {
	Publish(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) (PublishedDocuments, error)
	Delete(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) error
}

type PublishedDocuments struct {
	IssuerURL      string
	AzureResources []azworkloadidentityv1alpha1.AzureResource
}
