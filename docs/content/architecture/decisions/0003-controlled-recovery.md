---
title: "ADR 0003: Require controlled forward-only recovery"
description: Record the decision not to let normal reconciliation adopt a retained managed identity after Kubernetes object recreation.
---

# ADR 0003: Require controlled forward-only recovery

- **Status:** Accepted
- **Date:** 2026-08-20
- **Decision owners:** Project maintainers

## Context

A retained Azure managed identity can outlive its `WorkloadIdentity`. Recreating the same namespace and name produces the same deterministic Azure identity name but a new Kubernetes UID.

Automatically replacing the ownership UID would let ordinary namespaced reconciliation transfer an external identity without a separate administrative decision. Azure APIs also lack the transaction and ETag primitives needed to make a multi-system transfer atomic.

## Decision

Normal reconciliation reports `RecoveryRequired` and performs no Azure or ServiceAccount writes when the logical key matches an earlier UID.

Ownership transfer requires a cluster-scoped `WorkloadIdentityRecovery` containing exact live source and target evidence. Recovery persists a plan, writes fencing tags, becomes forward-only after mutation starts, read-verifies the Azure commit, checkpoints it in Kubernetes, and only then clears the fence.

Recovery permissions are separate from namespaced `WorkloadIdentity` permissions.

## Alternatives considered

### Automatically adopt on logical-key match

Rejected because a recreated namespaced object would gain implicit authority to transfer an Azure identity and its application permissions.

### Require administrators to retag Azure manually

Rejected because manual edits are difficult to validate, are not coordinated with ServiceAccount state, and can leave partial ownership evidence.

### Delete and recreate the Azure identity

Rejected as the only recovery path because retention is intended to preserve identity IDs, Azure role assignments, and dependent configuration.

## Consequences

- Recovery is explicit, auditable, and separately authorized.
- A retained identity can block normal reconciliation until an administrator acts.
- After external mutation begins, deletion means “finish then delete,” not cancellation.
- Status, finalizers, and Azure tags form a protocol that maintainers must evolve compatibly.
