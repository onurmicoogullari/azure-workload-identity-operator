# Manual Validation

Use this checklist for live OpenShift and Azure validation before calling a release production-ready.

## Prerequisites

- OpenShift cluster access with permissions to install the operator.
- Azure subscription available for test resources.
- Azure identity for the operator using `DefaultAzureCredential`.
- Azure permissions from `docs/permissions.md`.
- A service account signing key Secret readable by the operator through an explicit `Role`/`RoleBinding`.
- Cleanup-approved Azure resource group names for destructive tests.

## OIDCIssuer

- Install the operator on OpenShift.
- Create `OIDCIssuer/default` with `deletionPolicy: Retain`.
- Verify the operator creates or adopts the configured Azure resource group.
- Verify the operator creates or adopts the configured storage account.
- Verify the storage account has HTTPS-only, TLS 1.2, blob public access, and shared key access disabled.
- Verify the operator creates or updates the configured blob container with blob public access.
- Verify `.well-known/openid-configuration` is publicly readable.
- Verify `openid/v1/jwks` is publicly readable.
- Verify `OIDCIssuer/default.status.issuerURL` is set.
- Verify `OIDCIssuer/default.status.conditions[Ready]` is `True`.
- If enabled, verify `Authentication.config.openshift.io/cluster.spec.serviceAccountIssuer` is updated.

## WorkloadIdentity

- Create a `WorkloadIdentity` in a test namespace.
- Verify the ServiceAccount is created in the same namespace.
- Verify the ServiceAccount has `azure.workload.identity/use=true`.
- Verify the ServiceAccount has `azure.workload.identity/client-id`.
- Verify the Azure user assigned managed identity is created or adopted.
- Verify the Azure federated identity credential is created on the managed identity.
- Verify the federated identity credential issuer matches `OIDCIssuer/default.status.issuerURL`.
- Verify the federated identity credential subject matches `system:serviceaccount:<namespace>:<serviceaccount>`.
- Verify the federated identity credential audience is `api://AzureADTokenExchange`.
- Verify `WorkloadIdentity.status.clientID` is set.
- Verify `WorkloadIdentity.status.conditions[Ready]` is `True`.

## Token Exchange

- Run a Pod using the managed ServiceAccount.
- Verify the Azure Workload Identity webhook injects projected token material.
- From the Pod, authenticate to Azure with workload identity.
- From the Pod, call a low-risk Azure API allowed to the managed identity.

## Adoption

- Pre-create the ServiceAccount, then create a matching `WorkloadIdentity`.
- Verify the operator adopts and annotates the ServiceAccount.
- Delete the `WorkloadIdentity` with `deletionPolicy: Delete`.
- Verify the adopted ServiceAccount remains.
- Pre-create the Azure resource group and UAMI, then create a matching `WorkloadIdentity`.
- Verify the operator adds adoption tags.
- Delete the `WorkloadIdentity` with `deletionPolicy: Delete`.
- Verify the adopted UAMI and resource group remain.

## Deletion

- Delete a `WorkloadIdentity` with `deletionPolicy: Retain`.
- Verify the ServiceAccount, UAMI, and federated identity credential remain.
- Delete a `WorkloadIdentity` with `deletionPolicy: Delete` where the operator created all resources.
- Verify the federated identity credential is deleted.
- Verify the UAMI is deleted.
- Verify the ServiceAccount is deleted.
- Verify the resource group is deleted only when the operator created it.
- Delete `OIDCIssuer/default` with `deletionPolicy: Retain`.
- Verify Azure issuer resources remain.
- Delete `OIDCIssuer/default` with `deletionPolicy: Delete` where the operator created all resources.
- Verify the storage account is deleted.
- Verify the resource group is deleted only when the operator created it.

## Failure Cases

- Remove the signing key Secret permission and verify `OIDCIssuer` reports `Ready=False`.
- Configure invalid Azure permissions and verify status conditions expose the Azure error.
- Change the OIDC issuer storage account/container and verify `WorkloadIdentity` reconciles after `OIDCIssuer/default` changes.
- Change the ServiceAccount annotations manually and verify the operator restores them.
