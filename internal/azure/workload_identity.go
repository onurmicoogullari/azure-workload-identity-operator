package azure

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/workloadidentity"
)

const azureADTokenExchangeAudience = "api://AzureADTokenExchange"
const azureResourceKindResourceGroup = "ResourceGroup"
const azureResourceKindUserAssignedIdentity = "UserAssignedIdentity"
const azureResourceKindFederatedIdentityCredential = "FederatedIdentityCredential"
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

	credential, err := clients.ensureFederatedIdentityCredential(ctx, identity, managedIdentity, issuerURL, subject)
	if err != nil {
		return workloadidentity.ManagedIdentity{}, err
	}
	if credential.ID != nil {
		resources = append(resources, azworkloadidentityv1alpha1.AzureResource{ID: *credential.ID, Kind: azureResourceKindFederatedIdentityCredential})
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
	return m.DeleteWithOptions(ctx, identity, workloadidentity.DeleteOptions{})
}

func (m *WorkloadIdentityManager) DeleteWithOptions(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, options workloadidentity.DeleteOptions) error {
	if m.Credential == nil {
		return fmt.Errorf("azure credential is required")
	}

	clients, err := newIdentityClients(identity.Spec.Azure.SubscriptionID, m.Credential)
	if err != nil {
		return err
	}
	return clients.delete(ctx, identity, options)
}

func (c *identityClients) delete(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, options workloadidentity.DeleteOptions) error {
	az := identity.Spec.Azure
	uami, err := c.identities.Get(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, nil)
	if isNotFound(err) {
		return c.finalizeResourceGroup(ctx, identity, options)
	}
	if err != nil {
		return fmt.Errorf("get user assigned identity before delete: %w", err)
	}
	if err := validateRecordedUserAssignedIdentityContinuity(identity, uami.Identity); err != nil {
		return err
	}
	parentAuthorized := isResourceOwnedByWorkloadIdentity(uami.Tags, identity) || userAssignedIdentityMatchesStatus(identity, uami.Identity)
	if !parentAuthorized {
		return c.transferPreservedResourceGroup(ctx, identity, options)
	}

	credentialFound, deleteCredential, err := c.shouldDeleteFederatedIdentityCredential(ctx, identity)
	if err != nil {
		return err
	}
	if credentialFound && !deleteCredential {
		return c.transferPreservedResourceGroup(ctx, identity, options)
	}
	if deleteCredential {
		_, err = c.federatedCredentials.Delete(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, az.FederatedIdentityCredentialName, nil)
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("delete federated identity credential: %w", err)
		}
	}

	if options.PreserveUserAssignedIdentity {
		if err := c.transferUserAssignedIdentityOwnership(ctx, identity, uami.Identity, options.UserAssignedIdentitySuccessorUID); err != nil {
			return err
		}
	} else if wasWorkloadIdentityCreatedByOperator(uami.Tags, identity) {
		_, err = c.identities.Delete(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, nil)
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("delete user assigned identity: %w", err)
		}
	}

	return c.finalizeResourceGroup(ctx, identity, options)
}

type resourceGroupsClient interface {
	Get(context.Context, string, *armresources.ResourceGroupsClientGetOptions) (armresources.ResourceGroupsClientGetResponse, error)
	CreateOrUpdate(context.Context, string, armresources.ResourceGroup, *armresources.ResourceGroupsClientCreateOrUpdateOptions) (armresources.ResourceGroupsClientCreateOrUpdateResponse, error)
	Delete(context.Context, string) error
}

type azureResourceGroupsClient struct {
	*armresources.ResourceGroupsClient
}

func (c *azureResourceGroupsClient) Delete(ctx context.Context, resourceGroupName string) error {
	poller, err := c.BeginDelete(ctx, resourceGroupName, nil)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(ctx, nil)
	return err
}

type userAssignedIdentitiesClient interface {
	Get(context.Context, string, string, *armmsi.UserAssignedIdentitiesClientGetOptions) (armmsi.UserAssignedIdentitiesClientGetResponse, error)
	CreateOrUpdate(context.Context, string, string, armmsi.Identity, *armmsi.UserAssignedIdentitiesClientCreateOrUpdateOptions) (armmsi.UserAssignedIdentitiesClientCreateOrUpdateResponse, error)
	Update(context.Context, string, string, armmsi.IdentityUpdate, *armmsi.UserAssignedIdentitiesClientUpdateOptions) (armmsi.UserAssignedIdentitiesClientUpdateResponse, error)
	Delete(context.Context, string, string, *armmsi.UserAssignedIdentitiesClientDeleteOptions) (armmsi.UserAssignedIdentitiesClientDeleteResponse, error)
}

type federatedIdentityCredentialsClient interface {
	Get(context.Context, string, string, string, *armmsi.FederatedIdentityCredentialsClientGetOptions) (armmsi.FederatedIdentityCredentialsClientGetResponse, error)
	CreateOrUpdate(context.Context, string, string, string, armmsi.FederatedIdentityCredential, *armmsi.FederatedIdentityCredentialsClientCreateOrUpdateOptions) (armmsi.FederatedIdentityCredentialsClientCreateOrUpdateResponse, error)
	Delete(context.Context, string, string, string, *armmsi.FederatedIdentityCredentialsClientDeleteOptions) (armmsi.FederatedIdentityCredentialsClientDeleteResponse, error)
}

type identityClients struct {
	resourceGroups       resourceGroupsClient
	identities           userAssignedIdentitiesClient
	federatedCredentials federatedIdentityCredentialsClient
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

	return &identityClients{resourceGroups: &azureResourceGroupsClient{ResourceGroupsClient: resourceGroups}, identities: identities, federatedCredentials: federatedCredentials}, nil
}

func (c *identityClients) ensureUserAssignedIdentity(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) ([]azworkloadidentityv1alpha1.AzureResource, armmsi.Identity, error) {
	az := identity.Spec.Azure
	resources := make([]azworkloadidentityv1alpha1.AzureResource, 0, 3)

	rg, err := c.ensureResourceGroup(ctx, identity)
	if err != nil {
		return nil, armmsi.Identity{}, err
	}
	if rg.ID != nil {
		resources = append(resources, azworkloadidentityv1alpha1.AzureResource{ID: *rg.ID, Kind: azureResourceKindResourceGroup})
	}

	uami, err := c.identities.Get(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, nil)
	if isNotFound(err) {
		if recordedID, recorded := recordedAzureResourceID(identity, azureResourceKindUserAssignedIdentity); recorded &&
			strings.EqualFold(recordedID, desiredUserAssignedIdentityID(identity)) {
			return nil, armmsi.Identity{}, azureResourceOwnershipConflict(azureResourceKindUserAssignedIdentity, recordedID)
		}
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
		if continuityErr := validateRecordedUserAssignedIdentityContinuity(identity, uami.Identity); continuityErr != nil {
			return nil, armmsi.Identity{}, continuityErr
		}
		updated, updateErr := c.convergeUserAssignedIdentity(ctx, identity, uami.Identity)
		if updateErr != nil {
			return nil, armmsi.Identity{}, updateErr
		}
		uami.Identity = updated
	}
	if uami.ID != nil {
		resources = append(resources, azworkloadidentityv1alpha1.AzureResource{ID: *uami.ID, Kind: azureResourceKindUserAssignedIdentity})
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
	if hasConflictingOwnershipTags(rg.Tags) || isOperatorResourceOwnedByDifferentWorkloadIdentity(rg.Tags, string(identity.UID)) {
		return rg.ResourceGroup, nil
	}

	created := wasWorkloadIdentityCreatedByOperator(rg.Tags, identity)
	desiredTags := workloadIdentityTags(identity, created)
	if rg.Tags != nil && hasTags(rg.Tags, desiredTags) {
		return rg.ResourceGroup, nil
	}
	updated, updateErr := c.resourceGroups.CreateOrUpdate(ctx, az.ResourceGroupName, armresources.ResourceGroup{
		Location: rg.Location,
		Tags:     mergeTags(rg.Tags, desiredTags),
	}, nil)
	if updateErr != nil {
		return armresources.ResourceGroup{}, fmt.Errorf("update resource group tags: %w", updateErr)
	}
	return updated.ResourceGroup, nil
}

func (c *identityClients) convergeUserAssignedIdentity(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, uami armmsi.Identity) (armmsi.Identity, error) {
	if hasConflictingOwnershipTags(uami.Tags) || isOperatorResourceOwnedByDifferentWorkloadIdentity(uami.Tags, string(identity.UID)) {
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

func (c *identityClients) ensureFederatedIdentityCredential(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, parent armmsi.Identity, issuerURL, subject string) (armmsi.FederatedIdentityCredential, error) {
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
		_, hasRecordedCredential := recordedAzureResourceID(identity, azureResourceKindFederatedIdentityCredential)
		previouslyManaged := federatedIdentityCredentialRecordedInStatus(identity, existing.FederatedIdentityCredential) ||
			(!hasRecordedCredential && federatedIdentityCredentialMatchesStatus(identity, existing.FederatedIdentityCredential))
		parentAuthorized := isResourceOwnedByWorkloadIdentity(parent.Tags, identity) || userAssignedIdentityMatchesStatus(identity, parent)
		if previouslyManaged && parentAuthorized {
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

func (c *identityClients) shouldDeleteFederatedIdentityCredential(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity) (bool, bool, error) {
	az := identity.Spec.Azure
	existing, err := c.federatedCredentials.Get(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, az.FederatedIdentityCredentialName, nil)
	if isNotFound(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("get federated identity credential before delete: %w", err)
	}
	_, hasRecordedCredential := recordedAzureResourceID(identity, azureResourceKindFederatedIdentityCredential)
	return true, federatedIdentityCredentialRecordedInStatus(identity, existing.FederatedIdentityCredential) ||
		(!hasRecordedCredential && federatedIdentityCredentialMatchesStatus(identity, existing.FederatedIdentityCredential)), nil
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

func federatedIdentityCredentialRecordedInStatus(identity *azworkloadidentityv1alpha1.WorkloadIdentity, existing armmsi.FederatedIdentityCredential) bool {
	if existing.ID == nil || *existing.ID == "" {
		return false
	}
	for _, resource := range identity.Status.AzureResources {
		if resource.Kind == azureResourceKindFederatedIdentityCredential && strings.EqualFold(resource.ID, *existing.ID) {
			return true
		}
	}
	return false
}

func hasConflictingOwnershipTags(tags map[string]*string) bool {
	return tags != nil &&
		((tags[managedByTag] != nil && *tags[managedByTag] != operatorName) ||
			(tags[operatorAPIGroupTag] != nil && *tags[operatorAPIGroupTag] != operatorAPIGroupValue))
}

func isResourceOwnedByWorkloadIdentity(tags map[string]*string, identity *azworkloadidentityv1alpha1.WorkloadIdentity) bool {
	return tags != nil &&
		tags[managedByTag] != nil && *tags[managedByTag] == operatorName &&
		tags[operatorAPIGroupTag] != nil && *tags[operatorAPIGroupTag] == operatorAPIGroupValue &&
		tags[workloadIdentityUIDTag] != nil && *tags[workloadIdentityUIDTag] == string(identity.UID)
}

func recordedAzureResourceID(identity *azworkloadidentityv1alpha1.WorkloadIdentity, kind string) (string, bool) {
	for _, resource := range identity.Status.AzureResources {
		if resource.Kind == kind {
			return resource.ID, true
		}
	}
	return "", false
}

func validateRecordedUserAssignedIdentityContinuity(identity *azworkloadidentityv1alpha1.WorkloadIdentity, current armmsi.Identity) error {
	recordedID, recorded := recordedAzureResourceID(identity, azureResourceKindUserAssignedIdentity)
	if !recorded {
		return nil
	}
	if current.ID == nil {
		return azureResourceOwnershipConflict(azureResourceKindUserAssignedIdentity, "<unknown>")
	}
	if !strings.EqualFold(recordedID, *current.ID) {
		return nil
	}
	if userAssignedIdentityMatchesStatus(identity, current) {
		return nil
	}
	return azureResourceOwnershipConflict(azureResourceKindUserAssignedIdentity, *current.ID)
}

func userAssignedIdentityMatchesStatus(identity *azworkloadidentityv1alpha1.WorkloadIdentity, current armmsi.Identity) bool {
	if current.Properties == nil || identity.Status.ClientID == "" || identity.Status.PrincipalID == "" {
		return false
	}
	if !strings.EqualFold(identity.Status.ClientID, stringValue(current.Properties.ClientID)) ||
		!strings.EqualFold(identity.Status.PrincipalID, stringValue(current.Properties.PrincipalID)) {
		return false
	}
	return identity.Status.TenantID == "" || strings.EqualFold(identity.Status.TenantID, stringValue(current.Properties.TenantID))
}

func desiredUserAssignedIdentityID(identity *azworkloadidentityv1alpha1.WorkloadIdentity) string {
	az := identity.Spec.Azure
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ManagedIdentity/userAssignedIdentities/%s",
		az.SubscriptionID,
		az.ResourceGroupName,
		az.UserAssignedIdentityName,
	)
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

	if err := c.resourceGroups.Delete(ctx, az.ResourceGroupName); err != nil {
		return fmt.Errorf("delete resource group: %w", err)
	}
	return nil
}

func (c *identityClients) finalizeResourceGroup(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, options workloadidentity.DeleteOptions) error {
	if options.PreserveResourceGroup {
		return c.transferResourceGroupOwnership(ctx, identity, options.ResourceGroupSuccessorUID, true)
	}
	return c.deleteResourceGroupIfOwned(ctx, identity)
}

func (c *identityClients) transferPreservedResourceGroup(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, options workloadidentity.DeleteOptions) error {
	if !options.PreserveResourceGroup {
		return nil
	}
	return c.transferResourceGroupOwnership(ctx, identity, options.ResourceGroupSuccessorUID, false)
}

func (c *identityClients) transferResourceGroupOwnership(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, successorUID string, preserveDeletionProvenance bool) error {
	if successorUID == "" {
		return nil
	}
	az := identity.Spec.Azure
	rg, err := c.resourceGroups.Get(ctx, az.ResourceGroupName, nil)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get resource group before ownership transfer: %w", err)
	}
	if !wasWorkloadIdentityCreatedByOperator(rg.Tags, identity) {
		return nil
	}
	_, err = c.resourceGroups.CreateOrUpdate(ctx, az.ResourceGroupName, armresources.ResourceGroup{
		Location: rg.Location,
		Tags:     mergeTags(rg.Tags, workloadIdentityTagsForUID(successorUID, preserveDeletionProvenance)),
	}, nil)
	if err != nil {
		return fmt.Errorf("transfer resource group ownership: %w", err)
	}
	logf.FromContext(ctx).Info(
		"Transferred ResourceGroup ownership",
		"resourceGroup", az.ResourceGroupName,
		"successorUID", successorUID,
		"deleteWithSuccessor", preserveDeletionProvenance,
	)
	return nil
}

func (c *identityClients) transferUserAssignedIdentityOwnership(ctx context.Context, identity *azworkloadidentityv1alpha1.WorkloadIdentity, current armmsi.Identity, successorUID string) error {
	if successorUID == "" || !wasWorkloadIdentityCreatedByOperator(current.Tags, identity) {
		return nil
	}
	az := identity.Spec.Azure
	_, err := c.identities.Update(ctx, az.ResourceGroupName, az.UserAssignedIdentityName, armmsi.IdentityUpdate{
		Location: current.Location,
		Tags:     mergeTags(current.Tags, workloadIdentityTagsForUID(successorUID, true)),
	}, nil)
	if err != nil {
		return fmt.Errorf("transfer user assigned identity ownership: %w", err)
	}
	logf.FromContext(ctx).Info(
		"Transferred UserAssignedIdentity ownership",
		"resourceGroup", az.ResourceGroupName,
		"userAssignedIdentity", az.UserAssignedIdentityName,
		"successorUID", successorUID,
	)
	return nil
}

func workloadIdentityTags(identity *azworkloadidentityv1alpha1.WorkloadIdentity, createdByOperator bool) map[string]*string {
	return workloadIdentityTagsForUID(string(identity.UID), createdByOperator)
}

func workloadIdentityTagsForUID(uid string, createdByOperator bool) map[string]*string {
	return operatorOwnershipTags(workloadIdentityUIDTag, uid, createdByOperator)
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
