# Azure Permissions

The operator uses `DefaultAzureCredential`. In-cluster, this normally means the operator Pod runs with Azure Workload Identity or managed identity.

## Required Azure Permissions

The operator needs Azure Resource Manager permissions. `OIDCIssuer` also needs Azure Storage data-plane permissions.

### Resource Management

Required for reconciling `OIDCIssuer` Azure resources:

- read/create/update/delete resource groups
- read/create/update/delete storage accounts
- read/create/update/delete blob containers

If the resource group already exists, scope these permissions to that resource group.

If the resource group does not exist and the operator must create it, scope these permissions at the subscription level.

Required for reconciling `WorkloadIdentity` Azure resources:

- read/create/update/delete resource groups
- read/create/update/delete user assigned managed identities
- read/create/update/delete federated identity credentials

If the resource group already exists, scope these permissions to that resource group. If the operator must create the resource group, scope resource group permissions at the subscription level.

### Blob Uploads

Required for uploading OIDC documents:

- upload `.well-known/openid-configuration`
- upload `openid/v1/jwks`

Grant a data-plane role such as `Storage Blob Data Contributor` on the storage account or container.

The operator disables shared key access on managed storage accounts and uploads blobs using Microsoft Entra ID.

## Signing Key Secret Access

The default operator RBAC does not grant access to Secrets. Grant the operator access only to the exact Secret referenced by `spec.signingKey.secretRef` and, during rotation, the Secret referenced by `spec.signingKey.retiringSecretRef`.

When rotating signing keys, keep the active and retiring signing keys in the same namespace when possible so access can be granted with one namespace-scoped Role. If a retiring key lives in a different namespace, create a separate Role and RoleBinding in that namespace for that exact Secret.

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
    resourceNames:
      - "bound-service-account-signing-key"
      - "previous-bound-service-account-signing-key"
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

The operator reads the configured active and retiring signing key Secrets during reconciliation. To avoid broad cluster-wide Secret watch permissions, Secret changes are picked up by periodic reconciliation rather than an immediate Secret watch. The default interval is 5 minutes and can be changed with `--signing-key-refresh-interval`.

To rotate safely:

1. Add the old signing key to `spec.signingKey.retiringSecretRef`.
2. Change `spec.signingKey.secretRef` to the new active signing key.
3. Wait until the operator publishes both keys and `.status.signingKeys` shows the old key as `Retiring`.
4. Keep the retiring key configured for at least the longest possible service account token lifetime, plus enough time for one successful reconciliation.
5. Remove the retiring key reference and its Secret RBAC after tokens signed by that key can no longer be valid.

The operator does not automatically decide when a retiring key is safe to remove. That decision depends on the cluster token lifetime and any external consumers that may cache tokens or JWKS.

## Deletion Behavior

### OIDCIssuer

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

### WorkloadIdentity

When `spec.deletionPolicy` is `Retain`, the operator removes its finalizer and leaves Azure resources and the ServiceAccount in place.

When `spec.deletionPolicy` is `Delete`, the operator:

- deletes the federated identity credential named in `spec.azure.federatedIdentityCredentialName`
- deletes the user assigned managed identity only if it was created by the operator
- deletes the resource group only if it was created by the operator
- deletes the ServiceAccount only if it was created by the operator

Federated identity credentials do not support tags. The operator therefore treats the configured federated identity credential name as owned by the `WorkloadIdentity`: it is always converged and it is deleted when `spec.deletionPolicy` is `Delete`.

The operator tracks created/adopted `WorkloadIdentity` Azure resources using tags:

- `managed-by=az-workload-identity-operator`
- `workload-identity-uid=<kubernetes-uid>`
- `created-by-operator=true|false`
- `operator-api-group=workloadidentity.azure.micosolutions.se`

The operator tracks created/adopted ServiceAccounts using labels:

- `azure.workload.identity/use=true`
- `workloadidentity.azure.micosolutions.se/managed-by=az-workload-identity-operator`
- `workloadidentity.azure.micosolutions.se/workload-identity-uid=<kubernetes-uid>`
- `workloadidentity.azure.micosolutions.se/created-by-operator=true|false`

Adopted ServiceAccounts are annotated with the Azure client ID and tenant ID, but are not deleted by the operator.

The default manager RBAC grants ServiceAccount `get/list/watch/create/update/patch/delete`. It still does not grant Secret access.
