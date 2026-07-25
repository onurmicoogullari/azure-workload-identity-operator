package azure

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

const (
	azureADTokenExchangeAudience                 = "api://AzureADTokenExchange"
	azureResourceKindResourceGroup               = "ResourceGroup"
	azureResourceKindUserAssignedIdentity        = "UserAssignedIdentity"
	azureResourceKindFederatedIdentityCredential = "FederatedIdentityCredential"
	workloadIdentityKeyTag                       = "workload-identity-key"
	workloadIdentityUIDTag                       = "workload-identity-uid"
)

type WorkloadIdentityManager struct {
	Credential azcore.TokenCredential
	Scope      Scope
}

func (m *WorkloadIdentityManager) Ensure(
	ctx context.Context,
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
	issuerURL, subject string,
) (workloadidentity.ManagedIdentity, error) {
	if m.Credential == nil {
		return workloadidentity.ManagedIdentity{}, fmt.Errorf("azure credential is required")
	}
	if err := m.Scope.Validate(); err != nil {
		return workloadidentity.ManagedIdentity{}, fmt.Errorf("validate Azure scope: %w", err)
	}

	clients, err := newIdentityClients(m.Scope, m.Credential)
	if err != nil {
		return workloadidentity.ManagedIdentity{}, err
	}

	return clients.ensure(ctx, identity, issuerURL, subject)
}

func (m *WorkloadIdentityManager) Delete(
	ctx context.Context,
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
) error {
	if m.Credential == nil {
		return fmt.Errorf("azure credential is required")
	}
	if err := m.Scope.Validate(); err != nil {
		return fmt.Errorf("validate Azure scope: %w", err)
	}

	clients, err := newIdentityClients(m.Scope, m.Credential)
	if err != nil {
		return err
	}
	return clients.delete(ctx, identity)
}

type userAssignedIdentitiesClient interface {
	Get(context.Context, string, string, *armmsi.UserAssignedIdentitiesClientGetOptions) (armmsi.UserAssignedIdentitiesClientGetResponse, error)
	CreateOrUpdate(context.Context, string, string, armmsi.Identity, *armmsi.UserAssignedIdentitiesClientCreateOrUpdateOptions) (armmsi.UserAssignedIdentitiesClientCreateOrUpdateResponse, error)
	Delete(context.Context, string, string, *armmsi.UserAssignedIdentitiesClientDeleteOptions) (armmsi.UserAssignedIdentitiesClientDeleteResponse, error)
}

type federatedIdentityCredentialsClient interface {
	Get(context.Context, string, string, string, *armmsi.FederatedIdentityCredentialsClientGetOptions) (armmsi.FederatedIdentityCredentialsClientGetResponse, error)
	CreateOrUpdate(context.Context, string, string, string, armmsi.FederatedIdentityCredential, *armmsi.FederatedIdentityCredentialsClientCreateOrUpdateOptions) (armmsi.FederatedIdentityCredentialsClientCreateOrUpdateResponse, error)
}

type identityClients struct {
	scope                Scope
	resourceGroups       resourceGroupsClient
	identities           userAssignedIdentitiesClient
	federatedCredentials federatedIdentityCredentialsClient
}

func newIdentityClients(scope Scope, credential azcore.TokenCredential) (*identityClients, error) {
	resourceGroups, err := armresources.NewResourceGroupsClient(scope.subscriptionID, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create resource groups client: %w", err)
	}
	identities, err := armmsi.NewUserAssignedIdentitiesClient(scope.subscriptionID, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create user assigned identities client: %w", err)
	}
	federatedCredentials, err := armmsi.NewFederatedIdentityCredentialsClient(scope.subscriptionID, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create federated identity credentials client: %w", err)
	}

	return &identityClients{
		scope:                scope,
		resourceGroups:       resourceGroups,
		identities:           identities,
		federatedCredentials: federatedCredentials,
	}, nil
}

func (c *identityClients) ensure(
	ctx context.Context,
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
	issuerURL, subject string,
) (workloadidentity.ManagedIdentity, error) {
	resources, managedIdentity, err := c.ensureUserAssignedIdentity(ctx, identity)
	if err != nil {
		return workloadidentity.ManagedIdentity{}, err
	}

	credential, err := c.ensureFederatedIdentityCredential(ctx, identity, issuerURL, subject)
	if err != nil {
		return workloadidentity.ManagedIdentity{}, err
	}
	if credential.ID != nil {
		resources = append(resources, azworkloadidentityv1alpha1.AzureResource{
			ID:   *credential.ID,
			Kind: azureResourceKindFederatedIdentityCredential,
		})
	}

	result := workloadidentity.ManagedIdentity{AzureResources: resources}
	if managedIdentity.Properties != nil {
		result.ClientID = stringValue(managedIdentity.Properties.ClientID)
		result.PrincipalID = stringValue(managedIdentity.Properties.PrincipalID)
		result.TenantID = stringValue(managedIdentity.Properties.TenantID)
	}
	return result, nil
}

func (c *identityClients) ensureUserAssignedIdentity(
	ctx context.Context,
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
) ([]azworkloadidentityv1alpha1.AzureResource, armmsi.Identity, error) {
	name := userAssignedIdentityName(identity)
	if err := workloadidentity.ValidateUserAssignedIdentityName(name); err != nil {
		return nil, armmsi.Identity{}, fmt.Errorf("validate user assigned identity name: %w", err)
	}

	resources := make([]azworkloadidentityv1alpha1.AzureResource, 0, 3)

	resourceGroup, err := ensureResourceGroup(ctx, c.resourceGroups, c.scope)
	if err != nil {
		return nil, armmsi.Identity{}, err
	}
	if resourceGroup.ID != nil {
		resources = append(resources, azworkloadidentityv1alpha1.AzureResource{
			ID:   *resourceGroup.ID,
			Kind: azureResourceKindResourceGroup,
		})
	}

	response, err := c.identities.Get(ctx, c.scope.resourceGroupName, name, nil)
	if isNotFound(err) {
		if recordedID, recorded := recordedAzureResourceID(identity, azureResourceKindUserAssignedIdentity); recorded &&
			strings.EqualFold(recordedID, desiredUserAssignedIdentityID(c.scope, identity)) {
			return nil, armmsi.Identity{}, azureResourceOwnershipConflict(
				azureResourceKindUserAssignedIdentity,
				recordedID,
			)
		}

		created, createErr := c.identities.CreateOrUpdate(
			ctx,
			c.scope.resourceGroupName,
			name,
			armmsi.Identity{
				Location: to.Ptr(c.scope.location),
				Tags:     workloadIdentityTags(identity),
			},
			nil,
		)
		if createErr != nil {
			return nil, armmsi.Identity{}, fmt.Errorf("create user assigned identity: %w", createErr)
		}
		response.Identity = created.Identity
	} else if err != nil {
		return nil, armmsi.Identity{}, fmt.Errorf("get user assigned identity: %w", err)
	}

	if err := validateUserAssignedIdentityOwnership(identity, response.Identity); err != nil {
		return nil, armmsi.Identity{}, err
	}
	if response.ID != nil {
		resources = append(resources, azworkloadidentityv1alpha1.AzureResource{
			ID:   *response.ID,
			Kind: azureResourceKindUserAssignedIdentity,
		})
	}
	return resources, response.Identity, nil
}

func (c *identityClients) ensureFederatedIdentityCredential(
	ctx context.Context,
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
	issuerURL, subject string,
) (armmsi.FederatedIdentityCredential, error) {
	identityName := userAssignedIdentityName(identity)
	credentialName := identity.Spec.Azure.FederatedIdentityCredentialName
	desired := desiredFederatedIdentityCredential(issuerURL, subject)
	existing, err := c.federatedCredentials.Get(
		ctx,
		c.scope.resourceGroupName,
		identityName,
		credentialName,
		nil,
	)
	if err == nil && validateFederatedIdentityCredential(credentialName, existing.FederatedIdentityCredential, desired) == nil {
		return existing.FederatedIdentityCredential, nil
	}
	if err != nil && !isNotFound(err) {
		return armmsi.FederatedIdentityCredential{}, fmt.Errorf("get federated identity credential: %w", err)
	}
	reconciled, reconcileErr := c.federatedCredentials.CreateOrUpdate(
		ctx,
		c.scope.resourceGroupName,
		identityName,
		credentialName,
		desired,
		nil,
	)
	if reconcileErr != nil {
		if isNotFound(err) {
			return armmsi.FederatedIdentityCredential{}, fmt.Errorf("create federated identity credential: %w", reconcileErr)
		}
		return armmsi.FederatedIdentityCredential{}, fmt.Errorf("repair federated identity credential: %w", reconcileErr)
	}
	if validationErr := validateFederatedIdentityCredential(
		credentialName,
		reconciled.FederatedIdentityCredential,
		desired,
	); validationErr != nil {
		return armmsi.FederatedIdentityCredential{}, fmt.Errorf(
			"verify reconciled federated identity credential: %w",
			validationErr,
		)
	}
	return reconciled.FederatedIdentityCredential, nil
}

func (c *identityClients) delete(
	ctx context.Context,
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
) error {
	name := userAssignedIdentityName(identity)
	if err := workloadidentity.ValidateUserAssignedIdentityName(name); err != nil {
		// Ensure rejects this name before making any Azure calls, so there can
		// be no operator-owned identity to delete.
		return nil
	}

	uami, err := c.identities.Get(ctx, c.scope.resourceGroupName, name, nil)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get user assigned identity before delete: %w", err)
	}
	if err := validateUserAssignedIdentityOwnership(identity, uami.Identity); err != nil {
		return err
	}

	_, err = c.identities.Delete(ctx, c.scope.resourceGroupName, name, nil)
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("delete user assigned identity: %w", err)
	}
	return nil
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

func validateFederatedIdentityCredential(
	name string,
	existing, desired armmsi.FederatedIdentityCredential,
) error {
	if existing.Properties == nil || desired.Properties == nil {
		return workloadidentity.NewConflictError(
			workloadidentity.ReasonFederatedIdentityCredentialConflict,
			fmt.Sprintf("federated identity credential %q does not contain a complete trust tuple", name),
		)
	}

	existingIssuer := stringValue(existing.Properties.Issuer)
	desiredIssuer := stringValue(desired.Properties.Issuer)
	existingSubject := stringValue(existing.Properties.Subject)
	desiredSubject := stringValue(desired.Properties.Subject)
	existingAudiences := stringValues(existing.Properties.Audiences)
	desiredAudiences := stringValues(desired.Properties.Audiences)
	if existingIssuer == desiredIssuer &&
		existingSubject == desiredSubject &&
		slices.Equal(existingAudiences, desiredAudiences) {
		return nil
	}

	return workloadidentity.NewConflictError(
		workloadidentity.ReasonFederatedIdentityCredentialConflict,
		fmt.Sprintf(
			"federated identity credential %q has issuer %q, subject %q, and audiences %v; expected issuer %q, subject %q, and audiences %v",
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

func validateUserAssignedIdentityOwnership(
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
	current armmsi.Identity,
) error {
	expected := workloadIdentityTags(identity)
	logicalKey := tagValue(current.Tags, workloadIdentityKeyTag)
	currentUID := tagValue(current.Tags, workloadIdentityUIDTag)
	if logicalKey == *expected[workloadIdentityKeyTag] &&
		currentUID != "" &&
		currentUID != string(identity.UID) {
		return workloadidentity.NewConflictError(
			workloadidentity.ReasonRecoveryRequired,
			fmt.Sprintf(
				"UserAssignedIdentity %q belongs to an earlier instance of WorkloadIdentity %s/%s; recovery is required",
				userAssignedIdentityName(identity),
				identity.Namespace,
				identity.Name,
			),
		)
	}
	if !hasTags(current.Tags, expected) {
		return azureResourceOwnershipConflict(
			azureResourceKindUserAssignedIdentity,
			stringValue(current.ID),
		)
	}
	return nil
}

func workloadIdentityTags(
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
) map[string]*string {
	tags := operatorOwnershipTags(workloadIdentityUIDTag, string(identity.UID), true)
	tags[workloadIdentityKeyTag] = to.Ptr(
		workloadidentity.LogicalIdentityKey(identity.Namespace, identity.Name),
	)
	return tags
}

func userAssignedIdentityName(identity *azworkloadidentityv1alpha1.WorkloadIdentity) string {
	return workloadidentity.UserAssignedIdentityName(
		identity.Namespace,
		identity.Spec.Azure.UserAssignedIdentityName,
	)
}

func desiredUserAssignedIdentityID(
	scope Scope,
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
) string {
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ManagedIdentity/userAssignedIdentities/%s",
		scope.subscriptionID,
		scope.resourceGroupName,
		userAssignedIdentityName(identity),
	)
}

func recordedAzureResourceID(
	identity *azworkloadidentityv1alpha1.WorkloadIdentity,
	kind string,
) (string, bool) {
	for _, resource := range identity.Status.AzureResources {
		if resource.Kind == kind {
			return resource.ID, true
		}
	}
	return "", false
}

func azureResourceOwnershipConflict(kind, id string) error {
	if id == "" {
		id = "<unknown>"
	}
	return workloadidentity.NewConflictError(
		workloadidentity.ReasonAzureResourceOwnershipConflict,
		fmt.Sprintf("%s %q is not owned by this WorkloadIdentity", kind, id),
	)
}

func tagValue(tags map[string]*string, key string) string {
	if tags == nil {
		return ""
	}
	return stringValue(tags[key])
}

func stringValues(values []*string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, stringValue(value))
	}
	return result
}

func stringValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
