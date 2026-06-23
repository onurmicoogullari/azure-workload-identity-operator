# Azure Permissions

The operator uses `DefaultAzureCredential`. In-cluster, this normally means the operator Pod runs with Azure Workload Identity or managed identity.

## Required Azure Permissions

The operator needs both Azure Resource Manager permissions and Azure Storage data-plane permissions.

### Resource Management

Required for reconciling the `OIDCIssuer` Azure resources:

- read/create/update/delete resource groups
- read/create/update/delete storage accounts
- read/create/update/delete blob containers

If the resource group already exists, scope these permissions to that resource group.

If the resource group does not exist and the operator must create it, scope these permissions at the subscription level.

### Blob Uploads

Required for uploading OIDC documents:

- upload `.well-known/openid-configuration`
- upload `openid/v1/jwks`

Grant a data-plane role such as `Storage Blob Data Contributor` on the storage account or container.

The operator disables shared key access on managed storage accounts and uploads blobs using Microsoft Entra ID.

## Signing Key Secret Access

The default operator RBAC does not grant access to Secrets. Grant the operator access only to the exact Secret referenced by `spec.signingKey.secretRef`.

Example:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: az-workload-identity-operator-signing-key-reader
  namespace: openshift-kube-apiserver
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    resourceNames: ["bound-service-account-signing-key"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: az-workload-identity-operator-signing-key-reader
  namespace: openshift-kube-apiserver
subjects:
  - kind: ServiceAccount
    name: az-workload-identity-operator-controller-manager
    namespace: az-workload-identity-operator-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: az-workload-identity-operator-signing-key-reader
```

## Signing Key Changes

The operator reads the configured signing key Secret during reconciliation. To avoid broad cluster-wide Secret watch permissions, Secret changes are picked up by periodic reconciliation rather than an immediate Secret watch. The default interval is 5 minutes and can be changed with `--signing-key-refresh-interval`.

## Deletion Behavior

When `spec.deletionPolicy` is `Retain`, the operator removes its finalizer and leaves Azure resources in place.

When `spec.deletionPolicy` is `Delete`, the operator:

- deletes the storage account only if it was created by the operator
- deletes the resource group only if it was created by the operator

Blob containers are not deleted separately. They are removed when their operator-created storage account is deleted.

The operator tracks created/adopted Azure resources using tags:

- `managed-by=az-workload-identity-operator`
- `oidc-issuer-uid=<kubernetes-uid>`
- `created-by-operator=true|false`
- `operator-api-group=workloadidentity.azure.micosolutions.se`

Azure resources have a tag limit. If an adopted resource already has too many tags for the operator to add its ownership tags, reconciliation fails and the `OIDCIssuer` status condition contains the Azure error.
