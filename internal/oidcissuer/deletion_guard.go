package oidcissuer

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

const (
	ReasonBlockedByWorkloadIdentities                 = "BlockedByWorkloadIdentities"
	ReasonBlockedByClusterServiceAccountIssuer        = "BlockedByClusterServiceAccountIssuer"
	ReasonClusterServiceAccountIssuerGuardUnavailable = "ClusterServiceAccountIssuerGuardUnavailable"
	ReasonClusterServiceAccountIssuerCheckFailed      = "ClusterServiceAccountIssuerCheckFailed"
	ReasonBlockedByOpenShiftServiceAccountIssuer      = "BlockedByOpenShiftServiceAccountIssuer"
)

// DeletionGuardResult describes a deletion handoff decision without applying
// caller-specific effects such as status updates or admission errors.
type DeletionGuardResult struct {
	Blocked               bool
	CheckFailed           bool
	Reason                string
	Message               string
	Err                   error
	WorkloadIdentityCount int
}

type WorkloadIdentityLister interface {
	List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
}

type ServiceAccountTokenIssuerReader interface {
	CurrentIssuer(ctx context.Context) (string, error)
}

type OpenShiftServiceAccountIssuerReader interface {
	Get(ctx context.Context) (string, error)
}

func CheckWorkloadIdentityDeletionBlock(ctx context.Context, lister WorkloadIdentityLister, referenceLimit int) (DeletionGuardResult, error) {
	identities := &azworkloadidentityv1alpha1.WorkloadIdentityList{}
	if err := lister.List(ctx, identities); err != nil {
		return DeletionGuardResult{}, fmt.Errorf("list WorkloadIdentities before OIDCIssuer deletion: %w", err)
	}
	if len(identities.Items) == 0 {
		return DeletionGuardResult{}, nil
	}

	return DeletionGuardResult{
		Blocked:               true,
		Reason:                ReasonBlockedByWorkloadIdentities,
		Message:               workloadidentity.DeletionBlockedMessage(identities.Items, referenceLimit),
		WorkloadIdentityCount: len(identities.Items),
	}, nil
}

func CheckTokenIssuerHandoff(
	ctx context.Context,
	issuer *azworkloadidentityv1alpha1.OIDCIssuer,
	serviceAccountTokens ServiceAccountTokenIssuerReader,
	openShiftServiceAccountIssuer OpenShiftServiceAccountIssuerReader,
) (DeletionGuardResult, error) {
	if !HasPublishedIssuerURL(issuer) {
		return DeletionGuardResult{}, nil
	}
	if serviceAccountTokens == nil {
		if openShiftServiceAccountIssuer != nil {
			return DeletionGuardResult{}, nil
		}
		return DeletionGuardResult{
			Blocked: true,
			Reason:  ReasonClusterServiceAccountIssuerGuardUnavailable,
			Message: ClusterServiceAccountIssuerGuardUnavailableMessage(issuer.Status.IssuerURL),
		}, nil
	}

	currentIssuer, err := serviceAccountTokens.CurrentIssuer(ctx)
	if err != nil {
		return DeletionGuardResult{
			CheckFailed: true,
			Reason:      ReasonClusterServiceAccountIssuerCheckFailed,
			Message:     ClusterServiceAccountIssuerCheckFailedMessage(issuer.Status.IssuerURL, err),
			Err:         err,
		}, nil
	}
	if currentIssuer != issuer.Status.IssuerURL {
		return DeletionGuardResult{}, nil
	}

	return DeletionGuardResult{
		Blocked: true,
		Reason:  ReasonBlockedByClusterServiceAccountIssuer,
		Message: ClusterServiceAccountIssuerDeletionBlockedMessage(issuer.Status.IssuerURL),
	}, nil
}

func CheckOpenShiftIssuerHandoff(
	ctx context.Context,
	issuer *azworkloadidentityv1alpha1.OIDCIssuer,
	openShiftServiceAccountIssuer OpenShiftServiceAccountIssuerReader,
) (DeletionGuardResult, error) {
	if !HasPublishedIssuerURL(issuer) || openShiftServiceAccountIssuer == nil {
		return DeletionGuardResult{}, nil
	}

	currentIssuer, err := openShiftServiceAccountIssuer.Get(ctx)
	if err != nil {
		return DeletionGuardResult{}, fmt.Errorf("read OpenShift service account issuer before OIDCIssuer deletion: %w", err)
	}
	if currentIssuer != issuer.Status.IssuerURL {
		return DeletionGuardResult{}, nil
	}

	return DeletionGuardResult{
		Blocked: true,
		Reason:  ReasonBlockedByOpenShiftServiceAccountIssuer,
		Message: OpenShiftServiceAccountIssuerDeletionBlockedMessage(issuer.Status.IssuerURL),
	}, nil
}
