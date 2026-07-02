package azure

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"sigs.k8s.io/controller-runtime/pkg/client"

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/az-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/az-workload-identity-operator/internal/oidc"
	"github.com/onurmicoogullari/az-workload-identity-operator/internal/signingkey"
)

const oidcIssuerUIDTag = "oidc-issuer-uid"

type BlobOIDCDocumentPublisher struct {
	Client     client.Client
	Credential azcore.TokenCredential
}

func (p *BlobOIDCDocumentPublisher) Publish(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) (oidc.PublishedDocuments, error) {
	if p.Credential == nil {
		return oidc.PublishedDocuments{}, fmt.Errorf("azure credential is required")
	}
	if p.Client == nil {
		return oidc.PublishedDocuments{}, fmt.Errorf("kubernetes client is required")
	}

	clients, err := newStorageClients(issuer.Spec.Azure.SubscriptionID, p.Credential)
	if err != nil {
		return oidc.PublishedDocuments{}, err
	}

	resources, err := clients.ensureStorage(ctx, issuer)
	if err != nil {
		return oidc.PublishedDocuments{}, err
	}

	issuerURL := issuerURL(issuer)
	publicKeyPEM, err := signingkey.PublicKeyPEM(ctx, p.Client, issuer.Spec.SigningKey.SecretRef)
	if err != nil {
		return oidc.PublishedDocuments{}, err
	}

	signingAlgorithm, err := oidc.SigningAlgorithmFromPEM(publicKeyPEM)
	if err != nil {
		return oidc.PublishedDocuments{}, err
	}

	discovery, err := oidc.DiscoveryDocument(issuerURL, signingAlgorithm)
	if err != nil {
		return oidc.PublishedDocuments{}, err
	}

	jwks, err := oidc.JWKSFromPEM(publicKeyPEM)
	if err != nil {
		return oidc.PublishedDocuments{}, err
	}

	if err := clients.uploadJSON(ctx, issuer, ".well-known/openid-configuration", discovery); err != nil {
		return oidc.PublishedDocuments{}, err
	}
	if err := clients.uploadJSON(ctx, issuer, "openid/v1/jwks", jwks); err != nil {
		return oidc.PublishedDocuments{}, err
	}

	return oidc.PublishedDocuments{IssuerURL: issuerURL, AzureResources: resources}, nil
}

func (p *BlobOIDCDocumentPublisher) Delete(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) error {
	if p.Credential == nil {
		return fmt.Errorf("azure credential is required")
	}

	clients, err := newStorageClients(issuer.Spec.Azure.SubscriptionID, p.Credential)
	if err != nil {
		return err
	}

	az := issuer.Spec.Azure
	storage, err := clients.storageAccounts.GetProperties(ctx, az.ResourceGroupName, az.StorageAccountName, nil)
	if isNotFound(err) {
		return clients.deleteResourceGroupIfOwned(ctx, issuer)
	}
	if err != nil {
		return fmt.Errorf("get storage account before delete: %w", err)
	}
	if wasCreatedByOperator(storage.Tags, issuer) {
		_, err = clients.storageAccounts.Delete(ctx, az.ResourceGroupName, az.StorageAccountName, nil)
		if err != nil && !isNotFound(err) {
			return err
		}
	}

	return clients.deleteResourceGroupIfOwned(ctx, issuer)
}

type storageClients struct {
	resourceGroups  *armresources.ResourceGroupsClient
	storageAccounts *armstorage.AccountsClient
	blobContainers  *armstorage.BlobContainersClient
	credential      azcore.TokenCredential
}

func newStorageClients(subscriptionID string, credential azcore.TokenCredential) (*storageClients, error) {
	resourceGroups, err := armresources.NewResourceGroupsClient(subscriptionID, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create resource groups client: %w", err)
	}
	storageAccounts, err := armstorage.NewAccountsClient(subscriptionID, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create storage accounts client: %w", err)
	}
	blobContainers, err := armstorage.NewBlobContainersClient(subscriptionID, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create blob containers client: %w", err)
	}

	return &storageClients{resourceGroups: resourceGroups, storageAccounts: storageAccounts, blobContainers: blobContainers, credential: credential}, nil
}

func (c *storageClients) ensureStorage(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) ([]azworkloadidentityv1alpha1.AzureResource, error) {
	az := issuer.Spec.Azure
	resources := make([]azworkloadidentityv1alpha1.AzureResource, 0, 3)

	rg, err := c.ensureResourceGroup(ctx, issuer)
	if err != nil {
		return nil, err
	}
	if rg.ID != nil {
		resources = append(resources, azworkloadidentityv1alpha1.AzureResource{ID: *rg.ID, Kind: "ResourceGroup"})
	}

	storage, err := c.storageAccounts.GetProperties(ctx, az.ResourceGroupName, az.StorageAccountName, nil)
	if isNotFound(err) {
		poller, createErr := c.storageAccounts.BeginCreate(ctx, az.ResourceGroupName, az.StorageAccountName, armstorage.AccountCreateParameters{
			Location: to.Ptr(az.Location),
			Kind:     to.Ptr(armstorage.KindStorageV2),
			SKU:      &armstorage.SKU{Name: to.Ptr(armstorage.SKUNameStandardLRS)},
			Properties: &armstorage.AccountPropertiesCreateParameters{
				AccessTier:             to.Ptr(armstorage.AccessTierHot),
				AllowBlobPublicAccess:  to.Ptr(true),
				AllowSharedKeyAccess:   to.Ptr(false),
				EnableHTTPSTrafficOnly: to.Ptr(true),
				MinimumTLSVersion:      to.Ptr(armstorage.MinimumTLSVersionTLS12),
			},
			Tags: resourceTags(issuer, true),
		}, nil)
		if createErr != nil {
			return nil, fmt.Errorf("create storage account: %w", createErr)
		}
		created, waitErr := poller.PollUntilDone(ctx, nil)
		if waitErr != nil {
			return nil, fmt.Errorf("wait for storage account: %w", waitErr)
		}
		storage.Account = created.Account
	} else if err != nil {
		return nil, fmt.Errorf("get storage account: %w", err)
	} else {
		updated, updateErr := c.convergeStorageAccount(ctx, issuer, storage.Account)
		if updateErr != nil {
			return nil, updateErr
		}
		storage.Account = updated
	}
	if storage.ID != nil {
		resources = append(resources, azworkloadidentityv1alpha1.AzureResource{ID: *storage.ID, Kind: "StorageAccount"})
	}

	container, err := c.ensureBlobContainer(ctx, issuer)
	if err != nil {
		return nil, err
	}
	if container.ID != nil {
		resources = append(resources, azworkloadidentityv1alpha1.AzureResource{ID: *container.ID, Kind: "BlobContainer"})
	}

	return resources, nil
}

func (c *storageClients) ensureResourceGroup(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) (armresources.ResourceGroup, error) {
	az := issuer.Spec.Azure
	rg, err := c.resourceGroups.Get(ctx, az.ResourceGroupName, nil)
	if isNotFound(err) {
		created, createErr := c.resourceGroups.CreateOrUpdate(ctx, az.ResourceGroupName, armresources.ResourceGroup{
			Location: to.Ptr(az.Location),
			Tags:     resourceTags(issuer, true),
		}, nil)
		if createErr != nil {
			return armresources.ResourceGroup{}, fmt.Errorf("create resource group: %w", createErr)
		}
		return created.ResourceGroup, nil
	}
	if err != nil {
		return armresources.ResourceGroup{}, fmt.Errorf("get resource group: %w", err)
	}

	created := wasCreatedByOperator(rg.Tags, issuer)
	updated, updateErr := c.resourceGroups.CreateOrUpdate(ctx, az.ResourceGroupName, armresources.ResourceGroup{
		Location: rg.Location,
		Tags:     mergeTags(rg.Tags, resourceTags(issuer, created)),
	}, nil)
	if updateErr != nil {
		return armresources.ResourceGroup{}, fmt.Errorf("update resource group tags: %w", updateErr)
	}
	return updated.ResourceGroup, nil
}

func (c *storageClients) deleteResourceGroupIfOwned(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) error {
	az := issuer.Spec.Azure
	rg, err := c.resourceGroups.Get(ctx, az.ResourceGroupName, nil)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get resource group before delete: %w", err)
	}
	if !wasCreatedByOperator(rg.Tags, issuer) {
		return nil
	}

	poller, err := c.resourceGroups.BeginDelete(ctx, az.ResourceGroupName, nil)
	if err != nil {
		return fmt.Errorf("delete resource group: %w", err)
	}
	_, err = poller.PollUntilDone(ctx, nil)
	return err
}

func (c *storageClients) convergeStorageAccount(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer, account armstorage.Account) (armstorage.Account, error) {
	az := issuer.Spec.Azure
	properties := account.Properties
	needsUpdate := false

	desiredTags := resourceTags(issuer, wasCreatedByOperator(account.Tags, issuer))
	if account.Tags == nil || !hasTags(account.Tags, desiredTags) {
		needsUpdate = true
	}
	if properties == nil || properties.AllowBlobPublicAccess == nil || !*properties.AllowBlobPublicAccess {
		needsUpdate = true
	}
	if properties == nil || properties.AllowSharedKeyAccess == nil || *properties.AllowSharedKeyAccess {
		needsUpdate = true
	}
	if properties == nil || properties.EnableHTTPSTrafficOnly == nil || !*properties.EnableHTTPSTrafficOnly {
		needsUpdate = true
	}
	if properties == nil || properties.MinimumTLSVersion == nil || *properties.MinimumTLSVersion != armstorage.MinimumTLSVersionTLS12 {
		needsUpdate = true
	}
	if !needsUpdate {
		return account, nil
	}

	updated, err := c.storageAccounts.Update(ctx, az.ResourceGroupName, az.StorageAccountName, armstorage.AccountUpdateParameters{
		Tags: mergeTags(account.Tags, desiredTags),
		Properties: &armstorage.AccountPropertiesUpdateParameters{
			AllowBlobPublicAccess:  to.Ptr(true),
			AllowSharedKeyAccess:   to.Ptr(false),
			EnableHTTPSTrafficOnly: to.Ptr(true),
			MinimumTLSVersion:      to.Ptr(armstorage.MinimumTLSVersionTLS12),
		},
	}, nil)
	if err != nil {
		return armstorage.Account{}, fmt.Errorf("update storage account drift: %w", err)
	}
	return updated.Account, nil
}

func (c *storageClients) ensureBlobContainer(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) (armstorage.BlobContainer, error) {
	az := issuer.Spec.Azure
	container, err := c.blobContainers.Get(ctx, az.ResourceGroupName, az.StorageAccountName, az.BlobContainerName, nil)
	if isNotFound(err) {
		created, createErr := c.blobContainers.Create(ctx, az.ResourceGroupName, az.StorageAccountName, az.BlobContainerName, armstorage.BlobContainer{
			ContainerProperties: &armstorage.ContainerProperties{PublicAccess: to.Ptr(armstorage.PublicAccessBlob)},
		}, nil)
		if createErr != nil {
			return armstorage.BlobContainer{}, fmt.Errorf("create blob container: %w", createErr)
		}
		return created.BlobContainer, nil
	}
	if err != nil {
		return armstorage.BlobContainer{}, fmt.Errorf("get blob container: %w", err)
	}

	if container.ContainerProperties == nil || container.ContainerProperties.PublicAccess == nil || *container.ContainerProperties.PublicAccess != armstorage.PublicAccessBlob {
		updated, updateErr := c.blobContainers.Update(ctx, az.ResourceGroupName, az.StorageAccountName, az.BlobContainerName, armstorage.BlobContainer{
			ContainerProperties: &armstorage.ContainerProperties{PublicAccess: to.Ptr(armstorage.PublicAccessBlob)},
		}, nil)
		if updateErr != nil {
			return armstorage.BlobContainer{}, fmt.Errorf("update blob container drift: %w", updateErr)
		}
		return updated.BlobContainer, nil
	}

	return container.BlobContainer, nil
}

func (c *storageClients) uploadJSON(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer, name string, content []byte) error {
	az := issuer.Spec.Azure
	serviceURL := fmt.Sprintf("https://%s.blob.core.windows.net/", az.StorageAccountName)
	blobClient, err := azblob.NewClient(serviceURL, c.credential, nil)
	if err != nil {
		return fmt.Errorf("create blob client: %w", err)
	}
	contentType := "application/json"
	_, err = blobClient.UploadBuffer(ctx, az.BlobContainerName, name, content, &azblob.UploadBufferOptions{
		HTTPHeaders: &blob.HTTPHeaders{BlobContentType: &contentType},
	})
	if err != nil {
		return fmt.Errorf("upload %s: %w", name, err)
	}
	return nil
}

func issuerURL(issuer *azworkloadidentityv1alpha1.OIDCIssuer) string {
	az := issuer.Spec.Azure
	return fmt.Sprintf("https://%s.blob.core.windows.net/%s", az.StorageAccountName, az.BlobContainerName)
}

func resourceTags(issuer *azworkloadidentityv1alpha1.OIDCIssuer, createdByOperator bool) map[string]*string {
	return operatorOwnershipTags(oidcIssuerUIDTag, string(issuer.UID), createdByOperator)
}

func wasCreatedByOperator(existing map[string]*string, issuer *azworkloadidentityv1alpha1.OIDCIssuer) bool {
	return wasOperatorCreatedResource(existing, resourceTags(issuer, true))
}
