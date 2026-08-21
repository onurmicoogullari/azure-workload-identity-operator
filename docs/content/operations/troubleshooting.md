---
title: Troubleshooting
description: Diagnose installation, issuer, workload identity, recovery, webhook, and Azure authorization failures.
---

# Troubleshooting

Start with the resource condition, then inspect the controller log and the exact external object named in the message. Conditions are designed to identify whether the controller is waiting, blocked by ownership, or failing an operation.

## Collect a baseline

```bash
kubectl get oidcissuer,workloadidentity,workloadidentityrecovery --all-namespaces
kubectl get pods --all-namespaces | grep -E 'workload-identity|azure-workload'
kubectl get events --all-namespaces --sort-by=.lastTimestamp
```

```bash
kubectl logs \
  --namespace azure-workload-identity-operator-system \
  deployment/azure-workload-identity-operator-controller-manager \
  --container manager \
  --since=30m
```

## Manager Pod does not start

| Symptom | Check | Action |
| --- | --- | --- |
| `CreateContainerConfigError` | Referenced Azure credential Secret and its three keys | Create or repair the externally managed Secret. The existing Pod starts automatically. |
| Startup scope validation error | Mounted startup ConfigMap versus Helm Azure values | Restore the original scope values. Treat an intended change as a migration. |
| Webhook certificate mount missing | Selected provider and configured Secret in the webhook's namespace | Repair the cert-manager Certificate, self-managed TLS Secret, or OpenShift Service annotation for that webhook. |
| Azure credential error | Service Principal validity, workload identity injection, tenant | Repair the active `DefaultAzureCredential` path. |

## `OIDCIssuer` is not Ready

| Reason | Meaning | First check |
| --- | --- | --- |
| `InvalidName` | The object is not named `default`. | Recreate it with the supported name. |
| `PublishFailed` | Storage convergence, signing-key read, document generation, or blob upload failed. | Read the condition message and manager log; verify Azure permissions and the Secret key. |
| `OpenShiftIssuerClientNotConfigured` | OpenShift management was requested but the client is unavailable. | Confirm the Authentication API is discoverable or disable the OpenShift field. |
| `OpenShiftIssuerReadFailed` | `Authentication/cluster` could not be read. | Check API availability and RBAC. |
| `OpenShiftIssuerUpdateFailed` | The issuer setting could not be written. | Check admission, RBAC, and operator health. |
| `OpenShiftIssuerRolloutFailed` | The control-plane rollout did not become healthy. | Inspect the three OpenShift ClusterOperators. |

During deletion, `BlockedByWorkloadIdentities`, `BlockedByClusterServiceAccountIssuer`, `BlockedByOpenShiftServiceAccountIssuer`, or `ClusterServiceAccountIssuerGuardUnavailable` means the controller is deliberately preserving an issuer still in use.

## `WorkloadIdentity` is not Ready

| Reason | Meaning | Action |
| --- | --- | --- |
| `OIDCIssuerNotFound` or `OIDCIssuerNotReady` | The cluster trust root is unavailable. | Repair `OIDCIssuer/default`; the identity retries automatically. |
| `AzureEnsureFailed` | An Azure read or write failed. | Check condition text, Azure credentials, permissions, resource group, and API availability. |
| `AzureResourceOwnershipConflict` | The deterministic managed identity does not carry complete matching ownership. | Stop. Identify the actual owner; do not retag it casually. |
| `FederatedIdentityCredentialConflict` | An existing credential uses a different trust tuple. | Confirm no external writer owns it, then resolve the conflict deliberately. |
| `ServiceAccountConflict` | ServiceAccount annotations or ownership labels conflict. | Identify the current owner; do not remove evidence until ownership is understood. |
| `ServiceAccountReadFailed` or `ServiceAccountEnsureFailed` | Kubernetes read or patch failed. | Check RBAC, admission, resource quota, and API errors. |
| `RecoveryRequired` | A retained operator-created identity belongs to the previous UID of the same logical resource. | Follow [controlled recovery](../guides/recovery.md). |
| `RecoveryInProgress` | A recovery fence is active. | Monitor the existing `WorkloadIdentityRecovery`; normal writes are intentionally paused. |

## Recovery is blocked

Keep the same recovery object. `Blocked=True` is designed for correctable state such as an unavailable issuer, changed target, missing target after mutation, missing plan after a checkpoint, or ServiceAccount conflict.

After `mutationStarted=true`, do not attempt cancellation by removing finalizers. Restore the required state and let the recorded plan move forward.

## Pod is not mutated

Verify:

1. the Pod has `azure.workload.identity/use: "true"`;
2. `spec.serviceAccountName` names the reconciled ServiceAccount;
3. the mutating webhook Pods and selected certificate provider are Ready;
4. the `MutatingWebhookConfiguration` has a CA bundle; and
5. namespace selectors do not exclude the application namespace.

Existing Pods are not retroactively mutated. Recreate the Pod after fixing labels or webhook availability.

## Azure returns `403`

First determine whether the failure is token exchange or application
authorization. If `az account get-access-token --output none` succeeds,
federation works without printing the bearer token. Grant the workload
principal the missing role at its target Azure resource.

If exchange fails, compare issuer, subject, audience, client ID, and tenant ID exactly as described in [Verify and diagnose](../getting-started/verify.md).

## OpenShift sessions fail during issuer handoff

Authentication components roll when the service-account issuer changes. Open a new shell, re-run `eval $(crc oc-env)` when using CRC, log in again, and then inspect ClusterOperators. A temporary `Unauthorized` or connection reset during the rollout does not by itself prove operator failure.
