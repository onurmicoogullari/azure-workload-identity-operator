# Azure Permissions

The operator uses `DefaultAzureCredential`. The Helm chart supports initial
Service Principal bootstrap through an existing Kubernetes Secret and later
migration to an already-established workload or managed identity.

The disposable CRC e2e path is different from a production installation: by
default its Azure CLI identity creates an ephemeral Entra application and
Service Principal, grants temporary `Contributor` access at subscription scope
so the test can prove resource-group creation, and deletes the tracked role
assignments and application during cleanup. The Azure CLI identity therefore
needs Entra application create/delete permission and Azure role-assignment
create/delete permission. These broad test-orchestrator permissions are not a
production operator requirement.

## Required Azure Permissions

The operator needs Azure Resource Manager permissions. `OIDCIssuer` also needs Azure Storage data-plane permissions.

### Resource Management

The required `--azure-subscription-id`, `--azure-resource-group-name`, and
`--azure-location` startup flags define one platform-owned scope shared by
`OIDCIssuer` storage and all `WorkloadIdentity` managed identities. The Helm
chart maps `azure.subscriptionId`, `azure.resourceGroupName`, and
`azure.location` to those flags. These required, non-secret values are
installation identity and cannot change during an in-place Helm upgrade.

For Service Principal bootstrap, create a Secret in the release namespace with
`AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, and `AZURE_CLIENT_SECRET`, then set only
its name in `azure.credentials.existingSecret`. Do not put secret values in a
Helm values file. See the [chart documentation](../dist/chart/README.md) for
the workload-identity migration sequence.

Required for reconciling the shared scope and `OIDCIssuer` Azure resources:

- read/create resource groups
- read/create/update/delete storage accounts
- read/create/update/delete blob containers

If the resource group already exists, scope these permissions to that resource group.

If the resource group does not exist and the operator must create it, scope these permissions at the subscription level.

Required for reconciling `WorkloadIdentity` Azure resources in the same group:

- read/create/update/delete user assigned managed identities
- read/create/update/delete federated identity credentials

The operator creates the configured resource group if it is absent and accepts
it unchanged if it already exists. It never tags, transfers ownership of, or
deletes the shared resource group. If the group already exists, scope child
resource permissions to it. If the operator must create it, grant only the
required resource-group create/read actions at subscription scope.

### Blob Uploads

Required for uploading OIDC documents:

- upload `.well-known/openid-configuration`
- upload `openid/v1/jwks`

Grant a data-plane role such as `Storage Blob Data Contributor` on the storage account or container.

The operator disables shared key access on managed storage accounts and uploads blobs using Microsoft Entra ID.

## Signing Key Secret Access

The Helm chart grants the operator cluster-wide `get` access to Secrets so a
cluster-scoped `OIDCIssuer` can use active and retiring signing keys with
arbitrary names and namespaces. It does not grant Secret `list` or `watch`.
The operator uses a direct API reader and issues one named `GET` for each
configured `spec.signingKey.secretRef` and
`spec.signingKey.retiringSecretRef` during reconciliation.

Anyone allowed to create or update `OIDCIssuer` can therefore cause this
cluster-trusted controller to retrieve a Secret. Treat OIDCIssuer
administration as a cluster-administrator capability; the chart's optional
user-facing RBAC helpers are disabled by default.

## OIDCIssuer Refresh

The operator reads the configured active and retiring signing key Secrets during reconciliation. To avoid broad cluster-wide Secret watch permissions, signing key changes are picked up by periodic OIDCIssuer reconciliation rather than an immediate Secret watch. The same refresh also reconciles Azure storage resources and republishes OIDC documents. The default interval is 5 minutes and can be changed with `--oidc-issuer-refresh-interval`.

To rotate safely:

1. Add the old signing key to `spec.signingKey.retiringSecretRef`.
2. Change `spec.signingKey.secretRef` to the new active signing key.
3. Wait until the operator publishes both keys and `.status.signingKeys` shows the old key as `Retiring`.
4. Keep the retiring key configured for at least the longest possible service account token lifetime, plus enough time for one successful reconciliation.
5. Remove the retiring key reference and Secret after tokens signed by that key can no longer be valid.

The operator does not automatically decide when a retiring key is safe to remove. That decision depends on the cluster token lifetime and any external consumers that may cache tokens or JWKS.

## Deletion Behavior

### OIDCIssuer

When `spec.deletionPolicy` is `Retain`, the operator removes its finalizer and leaves Azure resources in place.

When `spec.deletionPolicy` is `Delete`, the operator:

- deletes the storage account only if it was created by the operator

Blob containers are not deleted separately. They are removed when their operator-created storage account is deleted.
The shared platform resource group is never deleted.

The operator tracks created/adopted Azure resources using tags:

- `managed-by=azure-workload-identity-operator`
- `oidc-issuer-uid=<kubernetes-uid>`
- `created-by-operator=true|false`
- `operator-api-group=workloadidentity.azure.micosolutions.se`

Azure resources have a tag limit. If an adopted resource already has too many tags for the operator to add its ownership tags, reconciliation fails and the `OIDCIssuer` status condition contains the Azure error.

### WorkloadIdentity

`WorkloadIdentity` periodically re-reads the shared Azure resource group, user assigned managed identity, and federated identity credential, then repairs authorized federated credential drift. The default base interval is 5 minutes and can be changed with `--workload-identity-refresh-interval`. Each resource receives stable jitter of up to 10% so reconciles do not all reach Azure simultaneously. When no drift exists, reconciliation performs Azure reads and updates the Kubernetes `status.lastReconciledTime`, but does not issue Azure writes. `lastReconciledTime` records reconciliation attempts, including attempts that result in `Ready=False`; use it together with the `Ready` condition and `observedGeneration`.

When `spec.deletionPolicy` is `Retain`, the operator removes its finalizer and leaves Azure resources and the ServiceAccount in place.

When `spec.deletionPolicy` is `Delete`, the operator:

- re-verifies complete user assigned managed identity ownership
- deletes the operator-created user assigned managed identity
- deletes the ServiceAccount only if it was created by the operator

Federated identity credentials are child resources of a user assigned managed
identity. After complete parent ownership verification, the operator deletes
the identity and relies on Azure parent-resource deletion to remove its
federated identity credentials.

The user assigned managed identity name resolves to
`<namespace>-<spec.azure.userAssignedIdentityName>`. The suffix and federated
credential name and `spec.serviceAccount.name` are immutable. Deleting and
recreating the configured ServiceAccount under the same namespace and name
remains supported. The resolved Azure name must be unique case-insensitively
across all `WorkloadIdentity` resources.

User assigned managed identities are exclusive and are never arbitrarily
adopted or shared. Normal reconciliation never retags or transfers one. The
separate, cluster-scoped `WorkloadIdentityRecovery` API can transfer an
operator-created retained identity only after exact source and target
verification; see [Controlled Workload Identity Recovery](recovery.md). Before
normal reconciliation changes a federated credential, the operator requires
every ownership tag from its initial identity read to match the current custom
resource. The logical key is lowercase hexadecimal SHA-256 of
`<namespace>/<WorkloadIdentity name>`.

The Managed Identity API exposes only a `CreateOrUpdate` operation for UAMIs;
it has no create-only request or resource ETag precondition. The supported
operating model therefore requires the operator to be the only Azure writer
creating or changing these deterministic UAMIs. This also applies to FIC and
UAMI deletion, for which the API exposes no resource ETags. The operator
does not perform additional ownership reads between the initial verified UAMI
read and a federated credential write. External writers in this resource group
would therefore violate the supported operating model.

Newly created identities carry these ownership tags:

- `managed-by=azure-workload-identity-operator`
- `workload-identity-uid=<kubernetes-uid>`
- `workload-identity-key=<sha256-logical-key>`
- `created-by-operator=true`
- `operator-api-group=workloadidentity.azure.micosolutions.se`

An existing identity with the same logical key but an earlier Kubernetes UID
sets `Ready=False` with reason `RecoveryRequired` and causes no UAMI, federated
credential, or ServiceAccount writes. Deleting that recreated custom resource
is allowed: the controller emits a warning and preserves all Azure and
ServiceAccount resources. Any other ownership mismatch reports
`AzureResourceOwnershipConflict` and performs no UAMI or federated credential
writes.

While controlled recovery is active, the UAMI also carries
`workload-identity-recovery-uid` and
`workload-identity-recovery-target-uid` fencing tags. Normal reconciliation
reports `RecoveryInProgress` and does not mutate Azure. A successful recovery
sets `workload-identity-last-recovery-uid` and changes
`workload-identity-uid` while retaining the in-progress tags. The operator
removes those tags only after the ownership commit is durably checkpointed in
Kubernetes.

The operator tracks created/adopted ServiceAccounts using labels:

- `azure.workload.identity/use=true`
- `workloadidentity.azure.micosolutions.se/managed-by=azure-workload-identity-operator`
- `workloadidentity.azure.micosolutions.se/workload-identity-uid=<WorkloadIdentity metadata.uid>`
- `workloadidentity.azure.micosolutions.se/created-by-operator=true|false`

The first successful relationship fixes the logical ServiceAccount provenance
in `status.serviceAccountProvenance`: `Created` when the ServiceAccount was
absent and the operator created it, or `Adopted` when it already existed. This
value remains stable when a ServiceAccount is deleted and recreated under the
same namespace and name. The labels mirror that persisted decision and are not
the normal source of truth. If reconciliation is interrupted after
ServiceAccount creation but before provenance is persisted, deletion uses
`created-by-operator=true` together with a matching `workload-identity-uid` as
a guarded crash-recovery fallback.

Before the relationship is established, the operator adopts only a
ServiceAccount without existing Azure client or tenant annotations. After
establishment, the configured namespace and name identify the logical
ServiceAccount: recreation does not change its provenance, and managed-label or
annotation differences are repaired as drift. Ownership-label conflicts, such
as a different `workload-identity-uid` or an operator-managed ServiceAccount
without an owner UID, are rejected. With deletion policy `Delete`, a `Created`
ServiceAccount is deleted and an `Adopted` ServiceAccount is retained.

The default manager RBAC grants ServiceAccount
`get/list/watch/create/update/patch/delete`. Its separate Secret rule grants
only `get`, as described in Signing Key Secret Access above.

## Bundled Azure Workload Identity Webhook RBAC

The chart runs the bundled mutating webhook in the reserved
`microsoft-azure-workload-identity-webhook-system` namespace. Its ServiceAccount has only
cluster-wide `get/list/watch` access to ServiceAccounts, which it needs to read
the annotations for Pods selected for mutation. cert-manager owns its serving
Secret and CA injection. The webhook ServiceAccount has no Secret permissions
and cannot update `MutatingWebhookConfiguration` objects.
