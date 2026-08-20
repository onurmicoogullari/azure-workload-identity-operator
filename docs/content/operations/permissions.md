---
title: Permissions
description: Scope the operator's Azure and Kubernetes permissions and understand the privileged API boundaries.
---

# Permissions

Separate the operator's platform permissions from each application's Azure authorization. The operator manages identity infrastructure; it does not grant application roles.

## Azure management-plane permissions

The startup flags and Helm values define one subscription, resource group, and location shared by issuer storage and workload managed identities.

Grant only the operations implemented by the controllers:

| Azure resource | Creation, adoption, and drift repair | Destructive cleanup |
| --- | --- | --- |
| Configured resource group | Get; create or update only when the group is absent | Never deleted or tagged |
| Storage account | Get, create, and update | Delete only for an operator-created account when its `OIDCIssuer` uses `deletionPolicy: Delete` |
| Blob container | Get, create, and update | Never deleted directly |
| User-assigned managed identity | Get, create, and update | Delete only for an operator-created identity when its `WorkloadIdentity` uses `deletionPolicy: Delete` |
| Federated identity credential | Get and create or update; list during controlled recovery | Never deleted directly |

The operator deletes an operator-created parent Storage account or managed
identity rather than deleting its container or federated credential child
individually. A custom role therefore does not need blob-container delete or
federated-credential delete actions for the current implementation.

If the resource group already exists, scope child-resource permissions to that group. If the operator must create it, grant the required resource-group create/read actions at subscription scope.

The operator never tags, transfers, or deletes the shared resource group.

## Azure Storage data plane

The operator uploads:

```text
.well-known/openid-configuration
openid/v1/jwks
```

Grant a data-plane role such as `Storage Blob Data Contributor` at the narrowest
usable scope. Managed Storage accounts disable shared-key access; uploads use
Microsoft Entra ID.

### Bootstrap the data-plane role

The account or container does not exist before the first issuer reconciliation,
so an account-scoped role assignment requires a staged bootstrap. Choose one
of these approaches:

1. **Inherited bootstrap:** assign the data-plane role at the dedicated resource
   group before creating `OIDCIssuer`. The future Storage account inherits it.
   This is simpler but grants data access to every Storage account in that
   resource group.
2. **Staged narrow scope:** grant only management-plane permissions, create
   `OIDCIssuer`, wait for the operator to create the Storage account, assign the
   data-plane role to the operator principal at that account or container, then
   wait for `Ready=True`. The issuer can remain not Ready while the role is
   absent or propagating; reconciliation resumes without recreating it.

The OpenShift end-to-end acceptance flow uses the staged account-scoped path.
The administrator performing the role assignment needs Azure authorization to
create role assignments; the in-cluster operator identity does not.

## Credential bootstrap

The chart can reference a Secret in the release namespace containing the
operator's client ID, tenant ID, and client secret. Configure its name and the
three non-empty data key selectors under `azure.credentials.secretRef`; the
chart maps those values to `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, and
`AZURE_CLIENT_SECRET`. The Secret is external to the Helm release.

For production, use an external secrets mechanism and grant read access only to the manager Pod through the normal Secret reference. Do not place credential values in chart values.

The disposable CRC end-to-end test uses a broader test-orchestrator identity that can create an ephemeral Entra application, assign temporary roles, and delete them. Those test permissions are not production operator requirements.

## Kubernetes controller permissions

The manager can:

- reconcile the three custom resources, status subresources, and finalizers;
- read all `WorkloadIdentity` objects to enforce uniqueness and issuer deletion guards;
- create, read, watch, patch, and delete ServiceAccounts;
- create a token for only the manager ServiceAccount in the release namespace
  so issuer deletion can inspect a freshly minted token's issuer;
- emit Kubernetes Events;
- read and patch OpenShift `Authentication/cluster` and read relevant ClusterOperators;
- read named signing-key Secrets; and
- create and manage Leases for leader election, with namespaced ConfigMap and
  core Event permissions retained by the chart's leader-election Role.

When secured metrics are enabled, the manager can also create Kubernetes
`TokenReview` and `SubjectAccessReview` requests to authenticate and authorize
metrics clients. The separate metrics-reader ClusterRole grants `/metrics`
access but is not bound to a reader by the chart.

## Signing-key Secret boundary

The manager ClusterRole grants cluster-wide Secret `get`, but not `list` or `watch`. A cluster-scoped `OIDCIssuer` may reference arbitrary names and namespaces, and the controller performs direct named reads during reconciliation.

Anyone who can create or update `OIDCIssuer` can therefore direct this cluster-trusted controller to retrieve a Secret. Treat issuer administration as a cluster-administrator capability. The optional user-facing RBAC helper roles are disabled by default.

## Recovery boundary

`WorkloadIdentityRecovery` is cluster scoped so recovery authority can be separated from ordinary namespaced identity creation. The chart provides viewer, editor, and admin ClusterRoles but binds none of them to users.

Grant a write-capable recovery role only to administrators authorized to transfer ownership of retained Azure identities.

## Bundled webhook permissions

The bundled mutating webhook has cluster-wide `get`, `list`, and `watch` on ServiceAccounts. It has no Secret permissions and cannot update `MutatingWebhookConfiguration` objects. cert-manager owns its serving Secret and CA injection.

## Application permissions

Grant workload-specific Azure roles to `WorkloadIdentity.status.principalID`. Keep those roles independent from the operator's infrastructure role and scope them to the narrowest target resource.
