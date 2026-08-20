---
title: OIDCIssuer API
description: Field, validation, status, and lifecycle reference for OIDCIssuer v1alpha1.
---

# `OIDCIssuer` API

`OIDCIssuer` is cluster scoped. The only supported name is `default`.

```yaml
apiVersion: workloadidentity.azure.micosolutions.se/v1alpha1
kind: OIDCIssuer
metadata:
  name: default
spec:
  azure:
    storageAccountName: oidcexample123
    blobContainerName: oidc
  signingKey:
    secretRef:
      namespace: openshift-kube-apiserver
      name: bound-service-account-signing-key
      key: service-account.pub
  openShift:
    updateServiceAccountIssuer: true
  deletionPolicy: Retain
```

## Spec

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `spec.azure` | object | Yes | — | Azure Storage publication settings. The entire object is immutable after creation. |
| `spec.azure.storageAccountName` | string | Yes | — | Globally unique Storage account name: 3–24 lowercase letters or digits. |
| `spec.azure.blobContainerName` | string | Yes | `oidc` | Public container name: 3–63 lowercase letters, digits, and interior hyphens. |
| `spec.signingKey` | object | Yes | — | Active and optional retiring service-account signing-key sources. |
| `spec.signingKey.secretRef` | object | Yes | — | Active signing key. |
| `spec.signingKey.retiringSecretRef` | object | No | — | Previous key retained in JWKS during token-validity overlap. Cannot equal `secretRef`. |
| `spec.openShift` | object | No | — | OpenShift-specific issuer integration. |
| `spec.openShift.updateServiceAccountIssuer` | boolean | No | `false` | Continuously reconcile `Authentication/cluster.spec.serviceAccountIssuer`. |
| `spec.deletionPolicy` | `Retain` or `Delete` | No | `Retain` | Whether to remove an operator-created Storage account after deletion guards pass. |

Every Secret reference contains required non-empty `namespace`, `name`, and
`key` strings. The example uses the public signer exercised by the OpenShift
4.22.8 acceptance path. Other distributions must expose their signer through a
platform-supported mechanism; there is no portable Kubernetes Secret name.

## Status

| Field | Description |
| --- | --- |
| `status.issuerURL` | Public issuer used in discovery and federated credentials. |
| `status.observedGeneration` | Latest spec generation handled by the controller. |
| `status.lastReconciledTime` | Last reconciliation attempt. |
| `status.azureResources[]` | Azure resource IDs and kinds observed during publication. |
| `status.signingKeys[]` | Published `kid`, algorithm, and `Active` or `Retiring` state. |
| `status.previousServiceAccountIssuer` | OpenShift issuer value captured before operator handoff. A present empty string is meaningful. |
| `status.conditions[]` | Standard Kubernetes conditions; currently includes `Ready`. |

## Admission and deletion

Admission rejects unsupported names, identical active and retiring references, and changes to `spec.azure`.

Deletion is rejected or held by the finalizer while workload identities exist, the cluster still mints tokens with this issuer, issuer verification is unavailable, or OpenShift still uses the URL.

See [Manage the OIDC issuer](../guides/oidc-issuer.md).
