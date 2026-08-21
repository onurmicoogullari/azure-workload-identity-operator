---
title: Permissions
description: Scope the operator's Azure and Kubernetes permissions and understand the privileged API boundaries.
---

# Permissions

Separate the operator's platform permissions from each application's Azure authorization. The operator manages identity infrastructure; it does not grant application roles.

## Azure management-plane permissions

The startup flags and Helm values define one subscription, resource group, and location shared by issuer storage and workload managed identities.

### Choose a bootstrap model

The operator does not create Azure role assignments. An administrator must
grant all management-plane and data-plane access before reconciliation needs
it. `User Access Administrator` would not enable self-bootstrap because the
operator does not call Azure RBAC APIs.

#### Pre-created resource group (recommended)

Before installation, create a dedicated resource group. Do not pre-create its
Storage account or managed identities; the operator creates and manages those
resources.

Assign the operator Service Principal:

| Role | Scope | Why the operator needs it |
| --- | --- | --- |
| [`Contributor`](https://learn.microsoft.com/azure/role-based-access-control/built-in-roles/privileged#contributor) | Dedicated resource group | Creates, reads, updates, and conditionally deletes Storage accounts and user-assigned managed identities. It also manages blob containers, federated identity credentials, and ownership tags. |
| [`Storage Blob Data Contributor`](https://learn.microsoft.com/azure/role-based-access-control/built-in-roles/storage#storage-blob-data-contributor) | Dedicated resource group | Gives the future operator-created Storage account inherited blob access for uploading the public OIDC discovery and JSON Web Key Set (JWKS) documents. |

This is the recommended model because both roles are limited to one dedicated
resource group while the operator retains full lifecycle management of every
child resource. `Storage Blob Data Contributor` applies to every Storage
account in the group, so do not place unrelated Storage accounts there.

#### Operator-created resource group

If the resource group and Storage account do not exist, the Service Principal
needs [`Contributor`](https://learn.microsoft.com/azure/role-based-access-control/built-in-roles/privileged#contributor)
at subscription scope so the operator can create the group and its resources.

For an unattended first reconciliation, it also needs
[`Storage Blob Data Contributor`](https://learn.microsoft.com/azure/role-based-access-control/built-in-roles/storage#storage-blob-data-contributor)
at subscription scope so the future Storage account inherits blob write access.
This grants the Service Principal blob data access to every Storage account in
the subscription.

To avoid subscription-wide blob data access, use a staged bootstrap: grant the
management role, create `OIDCIssuer`, wait for the operator to create the
Storage account, then have an administrator assign `Storage Blob Data
Contributor` on that account. The issuer remains not Ready until the role
assignment becomes effective and then resumes reconciliation automatically.

The operator still never needs `Owner`, `User Access Administrator`, or another
role that can create role assignments.

#### Custom roles

An equivalent custom role may replace `Contributor`, but it must cover all
operations in the table below and must be updated when the operator gains new
Azure behavior.

The person or automation performing the one-time bootstrap needs separate
permission to create the Service Principal and assign both roles. Those
bootstrap permissions must not be granted to the operator Service Principal.

### Implemented management operations

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

If the resource group already exists, scope child-resource permissions to that
group. If the operator must create it, resource-group create and read actions
must be granted at subscription scope. This management-plane access does not
authorize blob uploads. Unless `Storage Blob Data Contributor` is already
inherited from subscription scope, the operator creates the resource group and
Storage account but `OIDCIssuer` remains `Ready=False` when publication reaches
the blob data plane. An administrator must then assign the role to the Service
Principal on the new Storage account or blob container. Reconciliation resumes
automatically after the assignment becomes effective; the manager itself does
not stop.

The operator never tags, transfers, or deletes the shared resource group.

## Azure Storage data plane

The operator uploads:

```text
.well-known/openid-configuration
openid/v1/jwks
```

Grant `Storage Blob Data Contributor` at the narrowest usable scope. Managed
Storage accounts disable shared-key access; uploads use Microsoft Entra ID.

The OpenShift end-to-end acceptance flow assigns this role at account scope
after the operator creates the account. The administrator performing the role
assignment needs Azure authorization to create role assignments; the
in-cluster operator identity does not.

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

The bundled mutating webhook has cluster-wide `get`, `list`, and `watch` on
ServiceAccounts. It has no Secret permissions and cannot update
`MutatingWebhookConfiguration` objects. Certificate and CA ownership belongs
to the selected cert-manager, self-managed-certificate, or OpenShift service
CA path;
the webhook process does not rotate certificates itself.

## Application permissions

Grant workload-specific Azure roles to `WorkloadIdentity.status.principalID`. Keep those roles independent from the operator's infrastructure role and scope them to the narrowest target resource.
