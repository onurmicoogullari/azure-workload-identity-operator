---
title: Configure and test workload identity
description: Publish the cluster issuer, create a workload identity, and run a Pod with federated Azure authentication.
---

# Configure and test workload identity

This guide creates the minimum operator resources needed for a workload to exchange a projected Kubernetes service-account token for an Azure access token.

## Before you begin

Complete [Installation](./installation.md) and verify that the operator and both
admission webhooks are running. This guide assumes that the operator already has
working Azure credentials and the [required permissions](../operations/permissions.md).

## 1. Make the signing key available

`OIDCIssuer` reads a PEM public or private signing key from a named Secret. The
OpenShift 4.22.8 acceptance path uses the public key exposed by the
`openshift-kube-apiserver/bound-service-account-signing-key` Secret. The
operator does not generate or rotate the cluster signing key.

The operator has cluster-wide `get` permission for Secrets but cannot `list` or `watch` them. Restrict permission to administer `OIDCIssuer` to cluster administrators.

## 2. Create the issuer

Choose a globally unique Azure Storage account name containing only lowercase letters and numbers:

```yaml
apiVersion: workloadidentity.azure.micosolutions.se/v1alpha1
kind: OIDCIssuer
metadata:
  name: default
spec:
  azure:
    storageAccountName: '<globally-unique-name>'
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

On non-OpenShift clusters, omit `openShift` or set
`updateServiceAccountIssuer: false`. Expose a public signing key in a Secret
through the distribution's supported mechanism, update `secretRef` to that
exact location, and configure the service-account issuer through the
platform's supported mechanism. This path is compatibility preview rather than
the production acceptance path.

Apply and wait:

```bash
kubectl apply -f oidc-issuer.yaml
kubectl wait oidcissuer/default \
  --for=condition=Ready \
  --timeout=20m
kubectl get oidcissuer/default -o wide
```

Publishing can take several minutes when OpenShift rolls the API server after the issuer handoff.

## 3. Create a workload identity

Create the object in the application namespace:

```yaml
apiVersion: workloadidentity.azure.micosolutions.se/v1alpha1
kind: WorkloadIdentity
metadata:
  name: orders-api
  namespace: orders
spec:
  azure:
    userAssignedIdentityName: orders-api
    federatedIdentityCredentialName: kubernetes
  serviceAccount:
    name: orders-api
  deletionPolicy: Retain
```

The Azure managed identity name is taken directly from `userAssignedIdentityName`, here `orders-api`.

```bash
kubectl create namespace orders
kubectl apply -f workload-identity.yaml
kubectl wait workloadidentity/orders-api \
  --namespace orders \
  --for=condition=Ready \
  --timeout=10m
kubectl get workloadidentity/orders-api \
  --namespace orders \
  -o yaml
```

The operator creates or adopts the `orders-api` ServiceAccount, adds the Azure workload identity label and annotations, creates the Azure managed identity and federated credential, and records the resulting client, principal, tenant, issuer, and subject in status.

## 4. Run a workload

Pods must carry the Azure Workload Identity selection label and use the reconciled ServiceAccount:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: orders-api-check
  namespace: orders
  labels:
    azure.workload.identity/use: "true"
spec:
  serviceAccountName: orders-api
  restartPolicy: Never
  containers:
    - name: azure-cli
      image: mcr.microsoft.com/azure-cli:2.88.0@sha256:23b520868509add054d385d90dc3fc5268f10a2f58947a994e30babe938e31ae
      command:
        - /bin/sh
        - -c
        - |
          set -eu
          az login --service-principal \
            --username "$AZURE_CLIENT_ID" \
            --tenant "$AZURE_TENANT_ID" \
            --federated-token "$(cat "$AZURE_FEDERATED_TOKEN_FILE")" \
            --allow-no-subscriptions \
            --output none
          az account get-access-token \
            --resource https://management.azure.com/ \
            --output none
          printf '%s\n' 'Successfully acquired an Azure access token'
```

This example pins Azure CLI 2.88.0 to its multi-architecture manifest digest (Linux AMD64 and ARM64). When updating the CLI version, verify the new manifest's platforms and digest before changing the example. Grant the managed identity only the application permissions it needs.

## 5. Verify the result

```bash
kubectl logs --namespace orders pod/orders-api-check
kubectl get serviceaccount/orders-api --namespace orders -o yaml
kubectl get workloadidentity/orders-api --namespace orders -o yaml
```

Expected log output:

```text
Successfully acquired an Azure access token
```

The check deliberately suppresses Azure CLI token output. Never write the
`accessToken` field to Pod logs or a centrally collected logging system.

Continue with [Verify and diagnose](./verify.md) or the full [WorkloadIdentity guide](../guides/workload-identity.md).
