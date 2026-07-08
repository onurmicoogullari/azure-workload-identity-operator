package azure

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

const azureADTokenExchangeAudience = "api://AzureADTokenExchange"
const workloadIdentityUIDTag = "workload-identity-uid"

type WorkloadIdentityManager struct {
	Credential azcore.TokenCredential
}

func (m *WorkloadIdentityManager) Ensure(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, issuerURL, subject string) (workloadidentity.ManagedIdentity, error) {
	if m.Credential == nil {
		return workloadidentity.ManagedIdentity{}, fmt.Errorf("azure credential is required")
	}

	clients, err := newIdentityClients(identity.Spec.Azure.SubscriptionID, m.Credential)
	if err != nil {
		return workloadidentity.ManagedIdentity{}, err
	}

	resources, managedIdentity, err := clients.ensureUserAssignedIdentity(ctx, identity)
	if err != nil {
		return workloadidentity.ManagedIdentity{}, err
	}

	credential, err := clients.ensureFederatedIdentityCredential(ctx, identity, issuerURL, subject)
	if err != nil {
		return workloadidentity.ManagedIdentity{}, err
	}
	if credential.ID != nil {
		resources = append(resources, azworkloadidentityv1alpha1.AzureResource{ID: *credential.ID, Kind: "FederatedIdentityCredential"})
	}

	result := workloadidentity.ManagedIdentity{AzureResources: resources}
	if managedIdentity.Properties != nil {
		result.ClientID = stringValue(managedIdentity.Properties.ClientID)
		result.PrincipalID = stringValue(managedIdentity.Properties.PrincipalID)
		result.TenantID = stringValue(managedIdentity.Properties.TenantID)
	}
	return result, nil
}

func (m *WorkloadIdentityManager) Delete(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) error {
	if m.Credential == nil {
		return fmt.Errorf("azure credential is required")
	}

	clients, err := newIdentityClients(identity.Spec.Azure.SubscriptionID, m.Credential)
	if err != nil {
		return err
	}

	az := identity.Spec.Azure
	deleteCredential, err := clients.shouldDeleteFederatedIdentityCredential(ctx, identity)
	if err != nil {
		return err
	}
	if deleteCredential {
		_, err = clients.federatedCredentials.Delete(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, az.FederatedIdentityCredentialName, nil)
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("delete federated identity credential: %w", err)
		}
	}

	uami, err := clients.identities.Get(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, nil)
	if isNotFound(err) {
		return clients.deleteResourceGroupIfOwned(ctx, identity)
	}
	if err != nil {
		return fmt.Errorf("get user assigned identity before delete: %w", err)
	}
	if wasWorkloadIdentityCreatedByOperator(uami.Tags, identity) {
		_, err = clients.identities.Delete(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, nil)
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("delete user assigned identity: %w", err)
		}
	}

	return clients.deleteResourceGroupIfOwned(ctx, identity)
}

type identityClients struct {
	resourceGroups       *armresources.ResourceGroupsClient
	identities           *armmsi.UserAssignedIdentitiesClient
	federatedCredentials *armmsi.FederatedIdentityCredentialsClient
}

func newIdentityClients(subscriptionID string, credential azcore.TokenCredential) (*identityClients, error) {
	resourceGroups, err := armresources.NewResourceGroupsClient(subscriptionID, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create resource groups client: %w", err)
	}
	identities, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create user assigned identities client: %w", err)
	}
	federatedCredentials, err := armmsi.NewFederatedIdentityCredentialsClient(subscriptionID, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create federated identity credentials client: %w", err)
	}

	return &identityClients{resourceGroups: resourceGroups, identities: identities, federatedCredentials: federatedCredentials}, nil
}

func (c *identityClients) ensureUserAssignedIdentity(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) ([]azworkloadidentityv1alpha1.AzureResource, armmsi.Identity, error) {
	az := identity.Spec.Azure
	resources := make([]azworkloadidentityv1alpha1.AzureResource, 0, 3)

	rg, err := c.ensureResourceGroup(ctx, identity)
	if err != nil {
		return nil, armmsi.Identity{}, err
	}
	if rg.ID != nil {
		resources = append(resources, azworkloadidentityv1alpha1.AzureResource{ID: *rg.ID, Kind: "ResourceGroup"})
	}

	uami, err := c.identities.Get(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, nil)
	if isNotFound(err) {
		created, createErr := c.identities.CreateOrUpdate(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, armmsi.Identity{
			Location: to.Ptr(az.Location),
			Tags:     workloadIdentityTags(identity, true),
		}, nil)
		if createErr != nil {
			return nil, armmsi.Identity{}, fmt.Errorf("create user assigned identity: %w", createErr)
		}
		uami.Identity = created.Identity
	} else if err != nil {
		return nil, armmsi.Identity{}, fmt.Errorf("get user assigned identity: %w", err)
	} else {
		updated, updateErr := c.convergeUserAssignedIdentity(ctx, identity, uami.Identity)
		if updateErr != nil {
			return nil, armmsi.Identity{}, updateErr
		}
		uami.Identity = updated
	}
	if uami.ID != nil {
		resources = append(resources, azworkloadidentityv1alpha1.AzureResource{ID: *uami.ID, Kind: "UserAssignedIdentity"})
	}

	return resources, uami.Identity, nil
}

func (c *identityClients) ensureResourceGroup(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) (armresources.ResourceGroup, error) {
	az := identity.Spec.Azure
	rg, err := c.resourceGroups.Get(ctx, az.ResourceGroupName, nil)
	if isNotFound(err) {
		created, createErr := c.resourceGroups.CreateOrUpdate(ctx, az.ResourceGroupName, armresources.ResourceGroup{
			Location: to.Ptr(az.Location),
			Tags:     workloadIdentityTags(identity, true),
		}, nil)
		if createErr != nil {
			return armresources.ResourceGroup{}, fmt.Errorf("create resource group: %w", createErr)
		}
		return created.ResourceGroup, nil
	}
	if err != nil {
		return armresources.ResourceGroup{}, fmt.Errorf("get resource group: %w", err)
	}
	if isOperatorResourceOwnedByDifferentWorkloadIdentity(rg.Tags, string(identity.UID)) {
		return rg.ResourceGroup, nil
	}

	created := wasWorkloadIdentityCreatedByOperator(rg.Tags, identity)
	updated, updateErr := c.resourceGroups.CreateOrUpdate(ctx, az.ResourceGroupName, armresources.ResourceGroup{
		Location: rg.Location,
		Tags:     mergeTags(rg.Tags, workloadIdentityTags(identity, created)),
	}, nil)
	if updateErr != nil {
		return armresources.ResourceGroup{}, fmt.Errorf("update resource group tags: %w", updateErr)
	}
	return updated.ResourceGroup, nil
}

func (c *identityClients) convergeUserAssignedIdentity(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, uami armmsi.Identity) (armmsi.Identity, error) {
	if isOperatorResourceOwnedByDifferentWorkloadIdentity(uami.Tags, string(identity.UID)) {
		return uami, nil
	}
	desiredTags := workloadIdentityTags(identity, wasWorkloadIdentityCreatedByOperator(uami.Tags, identity))
	if uami.Tags != nil && hasTags(uami.Tags, desiredTags) {
		return uami, nil
	}

	az := identity.Spec.Azure
	updated, err := c.identities.Update(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, armmsi.IdentityUpdate{
		Location: uami.Location,
		Tags:     mergeTags(uami.Tags, desiredTags),
	}, nil)
	if err != nil {
		return armmsi.Identity{}, fmt.Errorf("update user assigned identity tags: %w", err)
	}
	return updated.Identity, nil
}

func (c *identityClients) ensureFederatedIdentityCredential(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, issuerURL, subject string) (armmsi.FederatedIdentityCredential, error) {
	az := identity.Spec.Azure
	desired := desiredFederatedIdentityCredential(issuerURL, subject)
	existing, err := c.federatedCredentials.Get(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, az.FederatedIdentityCredentialName, nil)
	if isNotFound(err) {
		created, createErr := c.federatedCredentials.CreateOrUpdate(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, az.FederatedIdentityCredentialName, desired, nil)
		if createErr != nil {
			return armmsi.FederatedIdentityCredential{}, fmt.Errorf("create federated identity credential: %w", createErr)
		}
		return created.FederatedIdentityCredential, nil
	}
	if err != nil {
		return armmsi.FederatedIdentityCredential{}, fmt.Errorf("get federated identity credential: %w", err)
	}
	if err := validateFederatedIdentityCredential(az.FederatedIdentityCredentialName, existing.FederatedIdentityCredential, desired); err != nil {
		if federatedIdentityCredentialMatchesStatus(identity, existing.FederatedIdentityCredential) {
			updated, updateErr := c.federatedCredentials.CreateOrUpdate(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, az.FederatedIdentityCredentialName, desired, nil)
			if updateErr != nil {
				return armmsi.FederatedIdentityCredential{}, fmt.Errorf("update federated identity credential: %w", updateErr)
			}
			return updated.FederatedIdentityCredential, nil
		}
		return armmsi.FederatedIdentityCredential{}, err
	}
	return existing.FederatedIdentityCredential, nil
}

func (c *identityClients) shouldDeleteFederatedIdentityCredential(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) (bool, error) {
	az := identity.Spec.Azure
	existing, err := c.federatedCredentials.Get(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, az.FederatedIdentityCredentialName, nil)
	if isNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get federated identity credential before delete: %w", err)
	}
	return federatedIdentityCredentialMatchesStatus(identity, existing.FederatedIdentityCredential), nil
}

func desiredFederatedIdentityCredential(issuerURL, subject string) armmsi.FederatedIdentityCredential {
	return armmsi.FederatedIdentityCredential{
		Properties: &armmsi.FederatedIdentityCredentialProperties{
			Issuer:    to.Ptr(issuerURL),
			Subject:   to.Ptr(subject),
			Audiences: []*string{to.Ptr(azureADTokenExchangeAudience)},
		},
	}
}

func federatedIdentityCredentialMatchesStatus(identity *azworkloadidentityv1alpha1.WorkloadIdentity, existing armmsi.FederatedIdentityCredential) bool {
	if identity.Status.IssuerURL == "" || identity.Status.Subject == "" {
		return false
	}
	desired := desiredFederatedIdentityCredential(identity.Status.IssuerURL, identity.Status.Subject)
	return validateFederatedIdentityCredential(identity.Spec.Azure.FederatedIdentityCredentialName, existing, desired) == nil
}

func validateFederatedIdentityCredential(name string, existing, desired armmsi.FederatedIdentityCredential) error {
	if existing.Properties == nil || desired.Properties == nil {
		return workloadidentity.NewConflictError(
			workloadidentity.ReasonFederatedIdentityCredentialConflict,
			fmt.Sprintf("federated identity credential %q already exists without a complete trust tuple", name),
		)
	}

	existingIssuer := stringValue(existing.Properties.Issuer)
	desiredIssuer := stringValue(desired.Properties.Issuer)
	existingSubject := stringValue(existing.Properties.Subject)
	desiredSubject := stringValue(desired.Properties.Subject)
	existingAudiences := stringValues(existing.Properties.Audiences)
	desiredAudiences := stringValues(desired.Properties.Audiences)
	if existingIssuer == desiredIssuer && existingSubject == desiredSubject && sameStrings(existingAudiences, desiredAudiences) {
		return nil
	}

	return workloadidentity.NewConflictError(
		workloadidentity.ReasonFederatedIdentityCredentialConflict,
		fmt.Sprintf(
			"federated identity credential %q already exists with issuer %q, subject %q, and audiences %v; expected issuer %q, subject %q, and audiences %v",
			name,
			existingIssuer,
			existingSubject,
			existingAudiences,
			desiredIssuer,
			desiredSubject,
			desiredAudiences,
		),
	)
}

func stringValues(values []*string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, stringValue(value))
	}
	return result
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (c *identityClients) deleteResourceGroupIfOwned(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) error {
	az := identity.Spec.Azure
	rg, err := c.resourceGroups.Get(ctx, az.ResourceGroupName, nil)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get resource group before delete: %w", err)
	}
	if !wasWorkloadIdentityCreatedByOperator(rg.Tags, identity) {
		return nil
	}

	poller, err := c.resourceGroups.BeginDelete(ctx, az.ResourceGroupName, nil)
	if err != nil {
		return fmt.Errorf("delete resource group: %w", err)
	}
	_, err = poller.PollUntilDone(ctx, nil)
	return err
}

func workloadIdentityTags(identity *azworkloadidentityv1alpha1.WorkloadIdentity, createdByOperator bool) map[string]*string {
	return operatorOwnershipTags(workloadIdentityUIDTag, string(identity.UID), createdByOperator)
}

func wasWorkloadIdentityCreatedByOperator(existing map[string]*string, identity *azworkloadidentityv1alpha1.WorkloadIdentity) bool {
	return wasOperatorCreatedResource(existing, workloadIdentityTags(identity, true))
}

func stringValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
