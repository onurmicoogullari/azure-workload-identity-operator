---
title: Status conditions
description: Interpret Ready, Progressing, Blocked, Failed, observed generation, and common condition reasons.
---

# Status conditions

All resources use Kubernetes `metav1.Condition`. Compare both the resource and condition `observedGeneration` with `metadata.generation` before acting on a condition.

## `OIDCIssuer` Ready reasons

| Reason | Status | Meaning |
| --- | --- | --- |
| `Published` | True | Documents and optional OpenShift integration are reconciled. |
| `InvalidName` | False | The resource is not named `default`. |
| `PublisherNotConfigured` | False | The controller was started without an OIDC document publisher. This indicates invalid process wiring. |
| `PublishFailed` | False | Azure storage, signing key, document generation, or upload failed. |
| `OpenShiftIssuerClientNotConfigured` | False | OpenShift management was requested without an available client. |
| `OpenShiftIssuerReadFailed` | False | Current OpenShift issuer read failed. |
| `OpenShiftIssuerUpdateFailed` | False | OpenShift issuer update failed. |
| `OpenShiftIssuerRolloutFailed` | False | Relevant OpenShift components did not complete rollout. |
| `BlockedByWorkloadIdentities` | False | Issuer deletion is waiting for all workload identities to be removed. |
| `BlockedByClusterServiceAccountIssuer` | False | New tokens still use this issuer. |
| `ClusterServiceAccountIssuerGuardUnavailable` | False | The controller cannot prove which issuer new tokens use. |
| `ClusterServiceAccountIssuerCheckFailed` | False | Minting or inspecting a token failed. |
| `BlockedByOpenShiftServiceAccountIssuer` | False | OpenShift still points at this issuer. |

## `WorkloadIdentity` Ready reasons

| Reason | Status | Meaning |
| --- | --- | --- |
| `Reconciled` | True | Azure identity, credential, and ServiceAccount are reconciled. |
| `OIDCIssuerNotFound` | False | `OIDCIssuer/default` is absent. |
| `OIDCIssuerNotReady` | False | The issuer exists but cannot currently supply a usable URL. |
| `ManagerNotConfigured` | False | The controller was started without an Azure workload identity manager. This indicates invalid process wiring. |
| `AzureEnsureFailed` | False | An Azure operation failed. |
| `AzureResourceOwnershipConflict` | False | Managed identity ownership evidence is not valid for this object. |
| `FederatedIdentityCredentialConflict` | False | Existing credential trust conflicts with desired trust. |
| `ServiceAccountReadFailed` | False | ServiceAccount inspection failed. |
| `ServiceAccountEnsureFailed` | False | ServiceAccount creation or patch failed. |
| `ServiceAccountConflict` | False | Existing annotations or ownership labels conflict. |
| `RecoveryRequired` | False | A retained identity belongs to an earlier instance of the same logical object. |
| `RecoveryInProgress` | False | The recovery controller owns the mutation fence. |
| `RecoveryCompleted` | False transiently | Recovery released the target; normal reconciliation will resume. |
| `RecoveryCancelled` | False | A pre-mutation recovery was cancelled; recovery remains required. |

## Recovery conditions

`WorkloadIdentityRecovery` uses four condition types:

- `Progressing`: current phase is advancing;
- `Blocked`: correctable state prevents progress;
- `Failed`: terminal failure before external mutation;
- `Complete`: verified ownership transfer and fence cleanup.

Common reasons include `PreflightComplete`, `RecoveryStarted`, `CommitVerified`, `RecoveryCompleted`, `DuplicateRecovery`, `OIDCIssuerNotReady`, `ServiceAccountConflict`, `TargetChanged`, `TargetNotFound`, and `RecoveryPlanMissing`.

Condition messages contain the specific object or mismatch. Treat the reason as a category and the message as the immediate diagnostic.
