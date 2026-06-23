package openshift

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const authenticationName = "cluster"

type ServiceAccountIssuerUpdater struct {
	Client client.Client
}

func (u *ServiceAccountIssuerUpdater) UpdateServiceAccountIssuer(ctx context.Context, issuerURL string) error {
	if u.Client == nil {
		return fmt.Errorf("kubernetes client is required")
	}

	authentication := &configv1.Authentication{}
	if err := u.Client.Get(ctx, client.ObjectKey{Name: authenticationName}, authentication); err != nil {
		return fmt.Errorf("get OpenShift Authentication %q: %w", authenticationName, err)
	}
	if authentication.Spec.ServiceAccountIssuer == issuerURL {
		return nil
	}

	patch := client.MergeFrom(authentication.DeepCopy())
	authentication.Spec.ServiceAccountIssuer = issuerURL
	if err := u.Client.Patch(ctx, authentication, patch); err != nil {
		return fmt.Errorf("update OpenShift service account issuer: %w", err)
	}
	return nil
}
