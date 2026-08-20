---
title: Publish overlapping signing keys
description: Keep old service-account tokens verifiable during an externally managed signer rotation.
---

# Publish overlapping signing keys

The operator publishes active and retiring public keys. It does not generate keys, rotate the cluster signer, detect the active private key, or decide when an old key is safe to remove.

## Rotation sequence

1. Follow the cluster platform's supported signing-key rotation procedure until the new public key is available in a Secret.
2. In one `OIDCIssuer` update, move the current key reference to `retiringSecretRef` and set `secretRef` to the new key.
3. Wait for the JWKS and status to contain both keys.
4. Complete the platform signer transition.
5. Verify newly minted tokens carry the new key ID.
6. Keep the retiring key published for the maximum old-token lifetime plus JWKS cache and reconciliation allowance.
7. Remove `retiringSecretRef`, then delete the old Secret when no old token can remain valid.

```yaml
spec:
  signingKey:
    secretRef:
      namespace: kube-system
      name: service-account-signing-key-new
      key: tls.key
    retiringSecretRef:
      namespace: kube-system
      name: service-account-signing-key-old
      key: tls.key
```

The active and retiring references cannot be identical. If both Secrets contain the same public key, the JWKS contains it only once.

## Verify overlap

```bash
kubectl get oidcissuer/default \
  -o jsonpath='{range .status.signingKeys[*]}{.state}{"\t"}{.algorithm}{"\t"}{.kid}{"\n"}{end}'
```

Expected during overlap:

```text
Active     RS256    <new-key-id>
Retiring   RS256    <old-key-id>
```

Fetch the JWKS from `status.issuerURL + /openid/v1/jwks` and confirm both `kid` values appear.

:::danger Do not guess the overlap window
The safe duration depends on the last time the old key signed a token, the maximum configured token lifetime, consumer caches, and platform-specific signer rollout behavior.
:::

For the production OpenShift target, use the Red Hat procedure for rotating Azure OIDC bound service-account signer keys as the source of truth for the platform half of this operation.
