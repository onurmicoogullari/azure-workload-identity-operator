---
title: Verify and diagnose
description: Confirm each link in the Kubernetes-to-Azure trust path after installation.
---

# Verify and diagnose

Verify the trust path from the control plane outward. A `Ready=True` condition proves reconciliation, but it does not prove that the application has the Azure role assignments needed for its own API calls.

## 1. Check operator health

```bash
kubectl get deployment,pod \
  --namespace azure-workload-identity-operator-system

kubectl logs \
  --namespace azure-workload-identity-operator-system \
  deployment/azure-workload-identity-operator-controller-manager \
  --container manager
```

With two replicas and leader election, only the leader actively reconciles.

## 2. Check the issuer

```bash
kubectl get oidcissuer/default \
  -o jsonpath='{.status.issuerURL}{"\n"}{.status.conditions}{"\n"}'
```

Fetch both public documents:

```bash
issuer_url="$(kubectl get oidcissuer/default -o jsonpath='{.status.issuerURL}')"
curl --fail --show-error --silent \
  "${issuer_url}/.well-known/openid-configuration"
curl --fail --show-error --silent \
  "${issuer_url}/openid/v1/jwks"
```

The discovery document's `issuer` must exactly equal `.status.issuerURL`. Its `jwks_uri` must be reachable without authentication.

## 3. Check the workload identity

```bash
kubectl get workloadidentity \
  --namespace '<application-namespace>' \
  '<name>' \
  -o yaml
```

Confirm:

- `Ready=True`;
- `observedGeneration` equals `metadata.generation`;
- `status.subject` is `system:serviceaccount:<namespace>:<serviceaccount>`;
- `status.issuerURL` matches the issuer; and
- `status.clientID`, `principalID`, and `tenantID` are populated.

## 4. Check webhook mutation

```bash
kubectl get pod \
  --namespace '<application-namespace>' \
  '<pod>' \
  -o yaml
```

A selected Pod should contain the projected token volume and Azure environment variables injected by the bundled mutating webhook. Check that the Pod itself has `azure.workload.identity/use: "true"`; the ServiceAccount label alone does not select Pod mutation.

## 5. Separate identity from authorization

If token exchange succeeds but an Azure API returns `403`, the workload identity is functioning and the managed identity lacks the required data-plane or management-plane role. Grant application permissions to the principal ID recorded in `WorkloadIdentity.status.principalID`.

If token exchange itself fails, compare the exact tuple:

| Value | Source |
| --- | --- |
| Issuer | `WorkloadIdentity.status.issuerURL` and OIDC discovery |
| Subject | `WorkloadIdentity.status.subject` |
| Audience | `api://AzureADTokenExchange` |
| Client ID | ServiceAccount annotation and `WorkloadIdentity.status.clientID` |
| Tenant ID | ServiceAccount annotation and `WorkloadIdentity.status.tenantID` |

See [Troubleshooting](../operations/troubleshooting.md) for condition reasons and failure-specific checks.
