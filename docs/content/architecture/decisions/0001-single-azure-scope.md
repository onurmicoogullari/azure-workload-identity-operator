---
title: "ADR 0001: Bind an installation to one Azure scope"
description: Record the decision to anchor subscription, resource group, and location as immutable installation identity.
---

# ADR 0001: Bind an installation to one Azure scope

- **Status:** Accepted
- **Date:** 2026-08-20
- **Decision owners:** Project maintainers

## Context

Issuer storage and every workload managed identity require an Azure subscription, resource group, and location. Treating those values as ordinary mutable chart configuration could redirect a running controller toward a second set of external resources while retained Kubernetes objects still describe the first set.

GitOps rendering makes template-time detection insufficient because Helm `lookup` may not observe the live cluster.

## Decision

One installation owns one Azure subscription, resource group, and location. The Helm chart writes that tuple to a retained immutable ConfigMap. Every manager Pod mounts and validates it before creating Kubernetes or Azure clients.

Changing the tuple requires an explicit migration to a deliberately anchored installation. It is not an in-place Helm upgrade or rollback.

## Alternatives considered

### Allow scope per custom resource

Rejected because it would expand credential and RBAC boundaries, complicate ownership checks, and let namespaced resources select subscriptions or resource groups.

### Treat Helm values as mutable process configuration

Rejected because template-time validation does not protect rendered YAML or GitOps workflows and cannot tie retained resources to their original scope.

### Store the scope only in custom-resource status

Rejected because startup must fail before clients and controllers become active, including when no individual custom resource has reconciled yet.

## Consequences

- Scope changes fail closed at Pod startup.
- Reinstall and rollback remain bound to retained external state.
- Multiple Azure scopes require multiple deliberately isolated installations and a future design for cluster-scoped resource separation.
- Administrators must protect deletion of the anchor ConfigMap.
