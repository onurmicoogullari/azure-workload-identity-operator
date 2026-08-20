---
title: WorkloadIdentityRecovery API
description: Field, plan, status, condition, and lifecycle reference for WorkloadIdentityRecovery v1alpha1.
---

# `WorkloadIdentityRecovery` API

`WorkloadIdentityRecovery` is cluster scoped and its entire spec is immutable.

```yaml
apiVersion: workloadidentity.azure.micosolutions.se/v1alpha1
kind: WorkloadIdentityRecovery
metadata:
  name: recover-reports-api
spec:
  workloadIdentityRef:
    namespace: reports
    name: reports-api
    uid: '<current-uid>'
  previousWorkloadIdentityUid: '<previous-uid>'
```

## Spec

| Field | Required | Description |
| --- | --- | --- |
| `spec.workloadIdentityRef.namespace` | Yes | Target namespace, DNS label syntax, maximum 63 characters. |
| `spec.workloadIdentityRef.name` | Yes | Target `WorkloadIdentity` name, DNS subdomain syntax, maximum 253 characters. |
| `spec.workloadIdentityRef.uid` | Yes | Exact UID of the current target object. |
| `spec.previousWorkloadIdentityUid` | Yes | Exact earlier UID published in target recovery evidence. Must differ from the current UID. |

Admission verifies the target exists, is not deleting, and reports current-generation `RecoveryRequired` with matching evidence. Duplicate previous UIDs are rejected when visible; the controller performs an authoritative duplicate check before mutation.

## Status checkpoints

| Field | Description |
| --- | --- |
| `status.observedGeneration` | Latest handled generation. The spec is immutable, but this remains a standard status checkpoint. |
| `status.lastAttemptTime` | Last recovery reconciliation attempt. |
| `status.startedTime` | First external mutation start. |
| `status.completedTime` | Read-verified completion time. |
| `status.mutationStarted` | Recovery is forward-only and the finalizer cannot be removed until completion. |
| `status.commitVerified` | Managed identity owner transfer was read-verified and checkpointed. |
| `status.plan` | Exact identity and trust tuple captured before mutation. |
| `status.conditions[]` | `Complete`, `Progressing`, `Blocked`, and `Failed`. |

## Persisted plan

`status.plan.userAssignedIdentity` records Azure resource ID, client ID, principal ID, and tenant ID. `status.plan.federatedIdentityCredential` records issuer, subject, and the audience set.

Every forward retry uses this plan instead of recomputing intent from potentially changed external state.

## Terminal states

- `Complete=True`: transfer and fence cleanup succeeded.
- `Failed=True`: the request ended before mutation, commonly because it lost the duplicate-recovery race or the target became invalid.

`Blocked=True` is non-terminal. Correct the reported conflict and keep the same object.

See [Recover a retained workload identity](../guides/recovery.md).
