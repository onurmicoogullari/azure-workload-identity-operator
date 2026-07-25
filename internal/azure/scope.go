package azure

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
)

var (
	azureSubscriptionIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	azureResourceGroupPattern  = regexp.MustCompile(`^[A-Za-z0-9_().-]*[A-Za-z0-9_()-]$`)
)

// Scope is the immutable platform-owned Azure boundary shared by all
// OIDCIssuer and WorkloadIdentity reconciliations.
type Scope struct {
	subscriptionID    string
	resourceGroupName string
	location          string
}

func NewScope(subscriptionID, resourceGroupName, location string) (Scope, error) {
	scope := Scope{
		subscriptionID:    subscriptionID,
		resourceGroupName: resourceGroupName,
		location:          location,
	}
	if err := scope.Validate(); err != nil {
		return Scope{}, err
	}
	return scope, nil
}

func (s Scope) Validate() error {
	if !azureSubscriptionIDPattern.MatchString(s.subscriptionID) {
		return fmt.Errorf("azure subscriptionId must be a UUID")
	}
	if len(s.resourceGroupName) < 1 || len(s.resourceGroupName) > 90 ||
		!azureResourceGroupPattern.MatchString(s.resourceGroupName) {
		return fmt.Errorf("azure resourceGroupName must be a valid Azure resource group name of at most 90 characters")
	}
	if strings.TrimSpace(s.location) == "" {
		return fmt.Errorf("azure location is required")
	}
	return nil
}

type resourceGroupsClient interface {
	Get(context.Context, string, *armresources.ResourceGroupsClientGetOptions) (armresources.ResourceGroupsClientGetResponse, error)
	CreateOrUpdate(context.Context, string, armresources.ResourceGroup, *armresources.ResourceGroupsClientCreateOrUpdateOptions) (armresources.ResourceGroupsClientCreateOrUpdateResponse, error)
}

func ensureResourceGroup(ctx context.Context, client resourceGroupsClient, scope Scope) (armresources.ResourceGroup, error) {
	resourceGroup, err := client.Get(ctx, scope.resourceGroupName, nil)
	if isNotFound(err) {
		created, createErr := client.CreateOrUpdate(ctx, scope.resourceGroupName, armresources.ResourceGroup{
			Location: to.Ptr(scope.location),
		}, nil)
		if createErr != nil {
			return armresources.ResourceGroup{}, fmt.Errorf("create resource group: %w", createErr)
		}
		return created.ResourceGroup, nil
	}
	if err != nil {
		return armresources.ResourceGroup{}, fmt.Errorf("get resource group: %w", err)
	}
	return resourceGroup.ResourceGroup, nil
}
