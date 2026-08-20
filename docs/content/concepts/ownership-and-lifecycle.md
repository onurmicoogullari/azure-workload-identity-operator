---
title: Ownership and lifecycle
description: Understand retention, ownership evidence, adoption, deletion, and the immutable Azure scope boundary.
---

# Ownership and lifecycle

The operator treats external-resource deletion as a privileged consequence of verified ownership. Kubernetes object existence alone is not enough evidence to delete Azure resources.

## Retain is the default

Both `OIDCIssuer` and `WorkloadIdentity` default to:

```yaml
spec:
  deletionPolicy: Retain
```

With `Retain`, the controller removes its finalizer and leaves external resources in place. This is the safest default for identity infrastructure because an accidental Kubernetes deletion should not immediately remove an Azure identity or issuer.

With `Delete`, cleanup remains ownership-aware:

| Resource | Deleted when requested | Always retained |
| --- | --- | --- |
| `OIDCIssuer` | Storage account created by this `OIDCIssuer` | Adopted storage account and shared resource group |
| `WorkloadIdentity` | Managed identity owned by the current object UID and operator-created ServiceAccount | Adopted ServiceAccount, shared resource group, and retained identity owned by an earlier object UID |

Federated credentials are children of managed identities and disappear with an operator-created parent identity.

## Azure ownership tags

Taggable top-level resources that the operator converges—Storage accounts and
user-assigned managed identities—use common tags:

```text
managed-by=azure-workload-identity-operator
operator-api-group=workloadidentity.azure.micosolutions.se
created-by-operator=true|false
```

Issuer storage also carries `oidc-issuer-uid`. Managed identities carry:

```text
workload-identity-uid=<Kubernetes object UID>
workload-identity-key=<SHA-256 of namespace/name>
```

The stable logical key distinguishes “the same named intent recreated” from an unrelated identity. The UID prevents a recreated custom resource from silently taking over retained Azure state.

The shared resource group is neither owned nor tagged by the operator. Blob
containers and federated identity credentials do not receive these tags;
their safety boundary comes from the verified parent Storage account or
managed identity plus their exact configured name and properties. Do not use a
tag inventory alone to infer ownership of every child resource.

## ServiceAccount creation and adoption

Before the relationship is established, the operator:

- creates the configured ServiceAccount if it does not exist; or
- adopts an existing ServiceAccount only when it has no Azure client or tenant annotations and no conflicting operator ownership labels.

It persists `Created` or `Adopted` in `status.serviceAccountProvenance`. Later ServiceAccount recreation does not change that decision. A `Created` ServiceAccount remains eligible for deletion under `deletionPolicy: Delete`; an `Adopted` one remains retained.

## Managed identities are exclusive

The Azure managed identity API exposes create-or-update without a create-only or resource-ETag precondition. The supported model therefore requires the operator to be the only Azure writer creating or changing its deterministic identities.

Normal reconciliation never retags or transfers an identity whose ownership evidence points elsewhere. It reports either:

- `AzureResourceOwnershipConflict` for unrelated or incomplete ownership; or
- `RecoveryRequired` when the logical key matches but the stored UID belongs to an earlier instance.

Only [controlled recovery](../guides/recovery.md) can perform the second transfer.
Deleting the recreated custom resource does not bypass that boundary: even
with `deletionPolicy: Delete`, `RecoveryRequired` preserves the earlier
instance's Azure resources and allows only the recreated object to finalize.

## Azure scope cannot drift

The Helm release records subscription ID, resource group name, and location in an immutable retained ConfigMap. Every manager Pod validates the mounted anchor before creating Azure or Kubernetes clients.

This makes failure safe across Helm, rendered YAML, and GitOps tools where template-time lookup may not observe live state. To change scope, create a deliberate migration plan and a separately anchored installation; do not delete the ConfigMap as an ordinary upgrade technique.
