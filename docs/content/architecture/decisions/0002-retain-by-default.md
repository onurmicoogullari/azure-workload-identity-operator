---
title: "ADR 0002: Retain external resources by default"
description: Record the decision to make deletion opt-in and ownership verified.
---

# ADR 0002: Retain external resources by default

- **Status:** Accepted
- **Date:** 2026-08-20
- **Decision owners:** Project maintainers

## Context

Kubernetes objects are easy to delete and can be removed by namespace cleanup, GitOps pruning, or operator error. Azure identities and a cluster issuer can remain dependencies for running workloads and valid tokens beyond the lifetime of a Kubernetes API object.

Automatic external deletion also becomes unsafe when an Azure resource pre-existed and was adopted or when ownership tags have changed.

## Decision

`OIDCIssuer` and `WorkloadIdentity` default to `deletionPolicy: Retain`.

`Delete` is an explicit request, and finalizers re-read external state and verify complete ownership before destructive actions. Shared resource groups, adopted Storage accounts, and adopted ServiceAccounts are never deleted by the operator.

The Helm chart retains CRDs and the startup scope anchor on uninstall.

## Alternatives considered

### Delete by default

Rejected because it turns routine Kubernetes lifecycle events into immediate external infrastructure deletion and increases the blast radius of GitOps pruning.

### Always retain

Rejected because deliberate decommissioning needs an auditable controller-driven cleanup path with ownership checks.

### Rely only on Kubernetes owner references

Rejected because Azure resources are outside Kubernetes garbage collection and adopted ServiceAccounts require provenance beyond object references.

## Consequences

- Accidental deletion usually leaves recoverable Azure state.
- Permanent decommissioning requires an ordered procedure.
- Retained managed identities need controlled recovery before reuse by a recreated object.
- Administrators must monitor for intentionally retained resources and decide their eventual disposition.
