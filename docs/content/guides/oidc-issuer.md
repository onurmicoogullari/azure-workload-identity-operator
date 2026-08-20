---
title: Manage the OIDC issuer
description: Publish OIDC documents, integrate OpenShift, inspect status, and safely hand off or delete the issuer.
---

# Manage the OIDC issuer

Create exactly one cluster-scoped `OIDCIssuer` named `default`. It represents the cluster's public token issuer and the Azure Storage resources used to publish its metadata.

## Configure publication on OpenShift

The production acceptance path uses the OpenShift-managed public
service-account signing key:

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

The signing-key value can be a PEM-encoded PKIX public key or a PEM private key
from which the operator derives the public key. Prefer public signer material
so the operator never needs access to the private signing key. Supported public
keys are RSA with `RS256` and P-256 ECDSA with `ES256`.

The controller creates or converges:

- a StorageV2 account with HTTPS-only traffic, TLS 1.2 minimum, public blob support, and shared-key access disabled;
- a public blob container; and
- `.well-known/openid-configuration` and `openid/v1/jwks` JSON blobs.

It authenticates blob uploads with Microsoft Entra ID.

### Non-OpenShift compatibility preview

There is no portable Kubernetes Secret name for the service-account signer.
Use the distribution's supported mechanism to expose a public signing key,
point `spec.signingKey.secretRef` to that exact Secret and key, and configure
the API server's service-account issuer through the platform-supported path.
Omit `spec.openShift` or keep `updateServiceAccountIssuer: false`.

## Understand OpenShift issuer management

Set:

```yaml
spec:
  openShift:
    updateServiceAccountIssuer: true
```

The controller captures the previous `Authentication/cluster.spec.serviceAccountIssuer`, sets it to the public issuer URL, waits for the kube-apiserver rollout, and continuously reconciles drift. Existing `oc` sessions can temporarily fail while authentication components roll.

On Kubernetes distributions without the OpenShift Authentication API, leave this field disabled and use the platform's supported issuer configuration.

## Inspect status

```bash
kubectl get oidcissuer/default -o yaml
```

Important fields:

- `status.issuerURL`: exact issuer used in every federated credential;
- `status.azureResources`: resource IDs observed during publication;
- `status.signingKeys`: key IDs, algorithms, and `Active` or `Retiring` state;
- `status.previousServiceAccountIssuer`: captured OpenShift restoration value;
- `status.observedGeneration`: latest handled spec generation; and
- `status.conditions[type=Ready]`: current reconciliation result.

The default refresh interval is five minutes. A spec update reconciles immediately; Secret data changes are observed at the next periodic refresh because the operator deliberately does not watch Secrets cluster-wide.

## Hand off the OpenShift issuer

Before decommissioning the issuer, stop the operator from reconciling the OpenShift setting.

1. Record `.status.previousServiceAccountIssuer`. A present empty string is a valid captured value.
2. Set `spec.openShift.updateServiceAccountIssuer: false` in the source of truth.
3. Wait until `status.observedGeneration` equals the new `metadata.generation`.
4. Confirm the field remains false and the generation has not changed.
5. Restore `Authentication/cluster.spec.serviceAccountIssuer`.
6. Wait until `kube-apiserver`, `authentication`, and `openshift-apiserver` are Available and not Progressing or Degraded.
7. Verify newly minted service-account tokens no longer carry the operator issuer.

```bash
generation="$(
  oc patch oidcissuer default --type=merge \
    -p '{"spec":{"openShift":{"updateServiceAccountIssuer":false}}}' \
    -o jsonpath='{.metadata.generation}'
)"

oc wait oidcissuer/default \
  --for=jsonpath='{.status.observedGeneration}'="$generation" \
  --timeout=10m
```

:::warning GitOps ownership
Change the declarative source first or suspend its reconciler. Otherwise it can re-enable issuer management during the handoff.
:::

## Delete safely

Deletion remains blocked while:

- any `WorkloadIdentity` exists;
- the cluster still mints service-account tokens with this issuer;
- the token-issuer guard is unavailable; or
- OpenShift still reports this issuer URL.

With `deletionPolicy: Delete`, the controller deletes only an operator-created Storage account. It never deletes the shared resource group or an adopted account.

See [Key rotation](./key-rotation.md) and [OIDCIssuer reference](../reference/oidcissuer.md).
