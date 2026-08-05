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

	azworkloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/oidc"
	"github.com/onurmicoogullari/azure-workload-identity-operator/internal/signingkey"
)

const oidcIssuerUIDTag = "oidc-issuer-uid"

type BlobOIDCDocumentPublisher struct {
	Reader     client.Reader
	Credential azcore.TokenCredential
	Scope      Scope
}

type oidcDocuments struct {
	Discovery   []byte
	JWKS        []byte
	SigningKeys []azworkloadidentityv1alpha1.SigningKeyStatus
}

func (p *BlobOIDCDocumentPublisher) Publish(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) (oidc.PublishedDocuments, error) {
	if p.Credential == nil {
		return oidc.PublishedDocuments{}, fmt.Errorf("azure credential is required")
	}
	if p.Reader == nil {
		return oidc.PublishedDocuments{}, fmt.Errorf("kubernetes reader is required")
	}
	if err := p.Scope.Validate(); err != nil {
		return oidc.PublishedDocuments{}, fmt.Errorf("validate Azure scope: %w", err)
	}

	clients, err := newStorageClients(p.Scope, p.Credential)
	if err != nil {
		return oidc.PublishedDocuments{}, err
	}

	resources, err := clients.ensureStorage(ctx, issuer)
	if err != nil {
		return oidc.PublishedDocuments{}, err
	}

	issuerURL := issuerURL(issuer)
	documents, err := buildOIDCDocuments(ctx, p.Reader, issuer, issuerURL)
	if err != nil {
		return oidc.PublishedDocuments{}, err
	}

	if err := clients.uploadJSON(ctx, issuer, ".well-known/openid-configuration", documents.Discovery); err != nil {
		return oidc.PublishedDocuments{}, err
	}
	if err := clients.uploadJSON(ctx, issuer, "openid/v1/jwks", documents.JWKS); err != nil {
		return oidc.PublishedDocuments{}, err
	}

	return oidc.PublishedDocuments{IssuerURL: issuerURL, AzureResources: resources, SigningKeys: documents.SigningKeys}, nil
}

func buildOIDCDocuments(ctx context.Context, reader client.Reader, issuer *azworkloadidentityv1alpha1.OIDCIssuer, issuerURL string) (oidcDocuments, error) {
	publicKeys, err := signingkey.PublicKeysPEM(ctx, reader, issuer.Spec.SigningKey)
	if err != nil {
		return oidcDocuments{}, err
	}

	signingKeys, algorithms, publicKeyPEMs, err := publishedSigningKeys(publicKeys)
	if err != nil {
		return oidcDocuments{}, err
	}

	discovery, err := oidc.DiscoveryDocument(issuerURL, algorithms...)
	if err != nil {
		return oidcDocuments{}, err
	}

	jwks, err := oidc.JWKSFromPEMs(publicKeyPEMs...)
	if err != nil {
		return oidcDocuments{}, err
	}

	return oidcDocuments{Discovery: discovery, JWKS: jwks, SigningKeys: signingKeys}, nil
}

func publishedSigningKeys(publicKeys []signingkey.PublicKey) ([]azworkloadidentityv1alpha1.SigningKeyStatus, []string, [][]byte, error) {
	signingKeys := make([]azworkloadidentityv1alpha1.SigningKeyStatus, 0, len(publicKeys))
	algorithms := make([]string, 0, len(publicKeys))
	publicKeyPEMs := make([][]byte, 0, len(publicKeys))
	seenKeyIDs := map[string]struct{}{}
	seenAlgorithms := map[string]struct{}{}

	for _, publicKey := range publicKeys {
		metadata, err := oidc.PublicKeyMetadataFromPEM(publicKey.PEM)
		if err != nil {
			return nil, nil, nil, err
		}
		if _, ok := seenKeyIDs[metadata.KeyID]; ok {
			continue
		}
		seenKeyIDs[metadata.KeyID] = struct{}{}
		signingKeys = append(signingKeys, azworkloadidentityv1alpha1.SigningKeyStatus{
			KID:       metadata.KeyID,
			Algorithm: metadata.Algorithm,
			State:     publicKey.State,
		})
		publicKeyPEMs = append(publicKeyPEMs, publicKey.PEM)

		if _, ok := seenAlgorithms[metadata.Algorithm]; ok {
			continue
		}
		seenAlgorithms[metadata.Algorithm] = struct{}{}
		algorithms = append(algorithms, metadata.Algorithm)
	}

	return signingKeys, algorithms, publicKeyPEMs, nil
}

func (p *BlobOIDCDocumentPublisher) Delete(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) error {
	if p.Credential == nil {
		return fmt.Errorf("azure credential is required")
	}
	if err := p.Scope.Validate(); err != nil {
		return fmt.Errorf("validate Azure scope: %w", err)
	}

	clients, err := newStorageClients(p.Scope, p.Credential)
	if err != nil {
		return err
	}

	az := issuer.Spec.Azure
	storage, err := clients.storageAccounts.GetProperties(ctx, p.Scope.resourceGroupName, az.StorageAccountName, nil)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get storage account before delete: %w", err)
	}
	if wasOIDCIssuerResourceCreatedByOperator(storage.Tags, issuer) {
		_, err = clients.storageAccounts.Delete(ctx, p.Scope.resourceGroupName, az.StorageAccountName, nil)
		if err != nil && !isNotFound(err) {
			return err
		}
	}

	return nil
}

type storageClients struct {
	scope           Scope
	resourceGroups  resourceGroupsClient
	storageAccounts *armstorage.AccountsClient
	blobContainers  *armstorage.BlobContainersClient
	credential      azcore.TokenCredential
}

func newStorageClients(scope Scope, credential azcore.TokenCredential) (*storageClients, error) {
	resourceGroups, err := armresources.NewResourceGroupsClient(scope.subscriptionID, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create resource groups client: %w", err)
	}
	storageAccounts, err := armstorage.NewAccountsClient(scope.subscriptionID, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create storage accounts client: %w", err)
	}
	blobContainers, err := armstorage.NewBlobContainersClient(scope.subscriptionID, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create blob containers client: %w", err)
	}

	return &storageClients{
		scope:           scope,
		resourceGroups:  resourceGroups,
		storageAccounts: storageAccounts,
		blobContainers:  blobContainers,
		credential:      credential,
	}, nil
}

func (c *storageClients) ensureStorage(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer) ([]azworkloadidentityv1alpha1.AzureResource, error) {
	az := issuer.Spec.Azure
	resources := make([]azworkloadidentityv1alpha1.AzureResource, 0, 3)

	rg, err := ensureResourceGroup(ctx, c.resourceGroups, c.scope)
	if err != nil {
		return nil, err
	}
	if rg.ID != nil {
		resources = append(resources, azworkloadidentityv1alpha1.AzureResource{ID: *rg.ID, Kind: "ResourceGroup"})
	}

	storage, err := c.storageAccounts.GetProperties(ctx, c.scope.resourceGroupName, az.StorageAccountName, nil)
	if isNotFound(err) {
		poller, createErr := c.storageAccounts.BeginCreate(ctx, c.scope.resourceGroupName, az.StorageAccountName, armstorage.AccountCreateParameters{
			Location: to.Ptr(c.scope.location),
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

func (c *storageClients) convergeStorageAccount(ctx context.Context, issuer *azworkloadidentityv1alpha1.OIDCIssuer, account armstorage.Account) (armstorage.Account, error) {
	az := issuer.Spec.Azure
	properties := account.Properties
	needsUpdate := false

	desiredTags := resourceTags(issuer, wasOIDCIssuerResourceCreatedByOperator(account.Tags, issuer))
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

	updated, err := c.storageAccounts.Update(ctx, c.scope.resourceGroupName, az.StorageAccountName, armstorage.AccountUpdateParameters{
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
	container, err := c.blobContainers.Get(ctx, c.scope.resourceGroupName, az.StorageAccountName, az.BlobContainerName, nil)
	if isNotFound(err) {
		created, createErr := c.blobContainers.Create(ctx, c.scope.resourceGroupName, az.StorageAccountName, az.BlobContainerName, armstorage.BlobContainer{
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
		updated, updateErr := c.blobContainers.Update(ctx, c.scope.resourceGroupName, az.StorageAccountName, az.BlobContainerName, armstorage.BlobContainer{
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

func wasOIDCIssuerResourceCreatedByOperator(existing map[string]*string, issuer *azworkloadidentityv1alpha1.OIDCIssuer) bool {
	return wasOperatorCreatedResource(existing, resourceTags(issuer, true))
}
