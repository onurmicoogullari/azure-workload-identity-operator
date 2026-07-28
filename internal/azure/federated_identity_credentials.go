package azure

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
)

type federatedIdentityCredentialsClientAdapter struct {
	*armmsi.FederatedIdentityCredentialsClient
}

func (a *federatedIdentityCredentialsClientAdapter) List(
	ctx context.Context,
	resourceGroupName, identityName string,
) ([]armmsi.FederatedIdentityCredential, error) {
	pager := a.NewListPager(resourceGroupName, identityName, nil)
	var credentials []armmsi.FederatedIdentityCredential
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list federated identity credentials: %w", err)
		}
		for _, credential := range page.Value {
			if credential != nil {
				credentials = append(credentials, *credential)
			}
		}
	}
	return credentials, nil
}
