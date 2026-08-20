---
title: WorkloadIdentity API
description: Field, validation, status, and naming reference for WorkloadIdentity v1alpha1.
---

# `WorkloadIdentity` API

`WorkloadIdentity` is namespaced. Its namespace participates in the Azure identity name and federated subject.

```yaml
apiVersion: workloadidentity.azure.micosolutions.se/v1alpha1
kind: WorkloadIdentity
metadata:
  name: reports-api
  namespace: reports
spec:
  azure:
    userAssignedIdentityName: api
    federatedIdentityCredentialName: kubernetes
  serviceAccount:
    name: reports-api
  deletionPolicy: Retain
```

## Spec

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `spec.azure.userAssignedIdentityName` | string | Yes | — | Suffix used in `<namespace>-<suffix>`. 1–128 characters before resolved-name validation; alphanumeric start, then alphanumeric, `_`, or `-`. Immutable. |
| `spec.azure.federatedIdentityCredentialName` | string | Yes | — | Azure child credential name, 1–120 supported characters. Immutable. |
| `spec.serviceAccount.name` | string | Yes | — | ServiceAccount to create or adopt. DNS subdomain syntax, maximum 253 characters. Immutable. |
| `spec.deletionPolicy` | `Retain` or `Delete` | No | `Retain` | Whether to delete verified operator-created resources. |

Admission also enforces:

- the resolved Azure identity name is 3–128 supported characters;
- resolved Azure identity names are unique case-insensitively across the cluster; and
- a ServiceAccount is referenced by at most one `WorkloadIdentity` in its namespace.

## Derived values

| Value | Formula |
| --- | --- |
| Managed identity name | `<metadata.namespace>-<spec.azure.userAssignedIdentityName>` |
| Subject | `system:serviceaccount:<metadata.namespace>:<spec.serviceAccount.name>` |
| Audience | `api://AzureADTokenExchange` |
| Logical identity key | Lowercase hexadecimal SHA-256 of `<namespace>/<WorkloadIdentity name>` |

## Status

| Field | Description |
| --- | --- |
| `status.clientID` | Managed identity client ID written to the ServiceAccount. |
| `status.principalID` | Azure principal to which application roles are assigned. |
| `status.tenantID` | Managed identity tenant. |
| `status.issuerURL` | Issuer used in the federated credential. |
| `status.subject` | Exact Kubernetes ServiceAccount subject. |
| `status.serviceAccountUID` | Last reconciled physical ServiceAccount UID. |
| `status.serviceAccountProvenance` | Stable logical provenance: `Created` or `Adopted`. |
| `status.recovery.previousWorkloadIdentityUid` | Earlier owner UID when controlled recovery is required. |
| `status.observedGeneration` | Latest handled spec generation. |
| `status.lastReconciledTime` | Last reconciliation attempt. |
| `status.azureResources[]` | Observed resource group, managed identity, and federated credential IDs. |
| `status.conditions[]` | Standard conditions; currently includes `Ready`. |

See [Manage workload identities](../guides/workload-identity.md).
