---
title: Manage workload identities
description: Create, adopt, reconcile, consume, and delete namespaced workload identities.
---

# Manage workload identities

A namespaced `WorkloadIdentity` binds one logical Kubernetes ServiceAccount to one Azure user-assigned managed identity and federated identity credential.

## Create an identity

```yaml
apiVersion: workloadidentity.azure.micosolutions.se/v1alpha1
kind: WorkloadIdentity
metadata:
  name: reports-api
  namespace: reports
spec:
  azure:
    userAssignedIdentityName: reports-api
    federatedIdentityCredentialName: kubernetes
  serviceAccount:
    name: reports-api
  deletionPolicy: Retain
```

The Azure identity name is exactly `reports-api`; the operator does not prepend the Kubernetes namespace. It must be unique case-insensitively across the installation and contain 3–128 supported characters.

The following fields are immutable:

- `spec.azure.userAssignedIdentityName`;
- `spec.azure.federatedIdentityCredentialName`; and
- `spec.serviceAccount.name`.

Create a new custom resource and migrate workloads when any of them must change.

## Create or adopt the ServiceAccount

If the ServiceAccount is absent, the operator creates it. If it exists, the operator adopts it only before the relationship is established and only when it has no Azure client or tenant annotations or conflicting ownership labels.

The operator writes:

```text
azure.workload.identity/use=true
azure.workload.identity/client-id=<managed identity client ID>
azure.workload.identity/tenant-id=<tenant ID>
```

It also writes operator ownership labels and persists `Created` or `Adopted` in `status.serviceAccountProvenance`.

After establishment, deleting and recreating the same ServiceAccount is supported. The controller repairs its annotations and labels without changing the original provenance.

## Select Pods for mutation

The bundled Microsoft webhook mutates a Pod only when the Pod carries:

```yaml
metadata:
  labels:
    azure.workload.identity/use: "true"
spec:
  serviceAccountName: reports-api
```

The ServiceAccount's label enables its use in the integration; it does not replace the Pod selection label.

## Grant application permissions

The operator creates identity plumbing but does not assign application roles. Read the principal ID:

```bash
principal_id="$(
  kubectl get workloadidentity/reports-api \
    --namespace reports \
    -o jsonpath='{.status.principalID}'
)"
```

Grant that principal the smallest Azure role at the narrowest application scope. Do not grant application access to the operator's own Service Principal.

## Drift reconciliation

Every successful resource receives a stable periodic requeue with up to 10% jitter around the five-minute default. The controller re-reads Azure state and:

- verifies managed identity ownership;
- repairs an absent or changed federated trust tuple only after ownership verification;
- repairs ServiceAccount annotations and labels; and
- updates status.

When no drift exists, reconciliation performs reads but no Azure writes.

## Inspect status

```bash
kubectl get workloadidentity/reports-api \
  --namespace reports \
  -o yaml
```

Use `Ready`, `observedGeneration`, and `lastReconciledTime` together. `lastReconciledTime` records attempts, including failed attempts; it is not a health signal by itself.

`status.recovery` appears only when the deterministic managed identity belongs to an earlier instance of the same logical resource. See [Controlled recovery](./recovery.md).

## Delete an identity

With `Retain`, deletion leaves the managed identity, federated credential, and ServiceAccount.

With `Delete`, deletion:

1. re-reads and fully verifies managed identity ownership;
2. deletes an operator-created managed identity and its child credentials; and
3. deletes the ServiceAccount only when its persisted provenance is `Created`.

Most ownership conflicts stop deletion. One deliberate exception protects a
retained identity from an earlier instance of the same logical
`WorkloadIdentity`: when ownership verification reports `RecoveryRequired`,
the controller preserves the Azure identity and ServiceAccount, emits a warning,
and removes the finalizer so the recreated custom resource can be deleted. It
does not transfer or delete the earlier instance's resources.

Never remove finalizers merely to bypass ownership checks; doing so abandons
external state without resolving its ownership.
