---
title: Install the operator
description: Install the supported OCI Helm chart with a fixed Azure scope and externally managed credentials.
---

# Install the operator

Install the OCI Helm chart into a dedicated namespace. The chart installs the operator, its three custom resource definitions (CRDs), validating webhooks, certificate resources, and the bundled Microsoft Azure Workload Identity mutating webhook.

## Before you begin

You need:

- an OpenShift or Kubernetes cluster with Helm;
- cert-manager installed and Ready;
- an Azure tenant, subscription, resource group name, and location;
- an Azure identity with the [required permissions](../operations/permissions.md); and
- an external mechanism that creates a Kubernetes Secret containing the operator's Azure client ID, tenant ID, and client secret. You will configure the Secret name and all three data keys explicitly.

:::note Platform support
OpenShift 4.22.8 is the production acceptance target. Other Kubernetes distributions remain compatibility preview. See [Compatibility and support](../operations/compatibility.md).
:::

## Install the published chart

Install the release before creating the credential Secret. Helm creates the
operator namespace and all release resources. OCI charts do not use
`helm repo add`:

```bash
helm upgrade --install azure-workload-identity-operator \
  oci://ghcr.io/onurmicoogullari/charts/azure-workload-identity-operator \
  --version '<version>' \
  --namespace azure-workload-identity-operator-system \
  --create-namespace \
  --set-string azure.tenantId='<tenant-id>' \
  --set-string azure.subscriptionId='<subscription-id>' \
  --set-string azure.resourceGroupName='<resource-group>' \
  --set-string azure.location='<location>' \
  --set-string azure.credentials.secretRef.name=azure-workload-identity-operator-azure-credentials \
  --set-string azure.credentials.secretRef.keys.clientId=AZURE_CLIENT_ID \
  --set-string azure.credentials.secretRef.keys.tenantId=AZURE_TENANT_ID \
  --set-string azure.credentials.secretRef.keys.clientSecret=AZURE_CLIENT_SECRET
```

Do not add `--wait` to this initial bootstrap command unless the Secret already
exists. Pin an exact chart version; a release chart pins the validated
multi-platform manager image by digest.

## Provide the credential Secret

The Deployment now exists, but its required `secretKeyRef` values cannot be
resolved yet. The manager Pods remain pending with
`CreateContainerConfigError`; this is not `CrashLoopBackOff`, because the
manager containers have not started. The kubelet retries automatically, so the
release does not need to be reinstalled.

You can observe this expected bootstrap state with:

```bash
kubectl get pods \
  --namespace azure-workload-identity-operator-system
```

For production, have an external secrets controller create the Secret. Keep
credential values out of Git, Helm values, shell history, and Argo CD
parameters.

For an interactive bootstrap, create the Secret directly in the namespace that
Helm created:

```bash
printf 'Azure client secret: '
IFS= read -r -s AZURE_CLIENT_SECRET
printf '\n'

printf '%s' "$AZURE_CLIENT_SECRET" | \
  kubectl create secret generic azure-workload-identity-operator-azure-credentials \
  --namespace azure-workload-identity-operator-system \
  --from-literal=AZURE_CLIENT_ID='<client-id>' \
  --from-literal=AZURE_TENANT_ID='<tenant-id>' \
  --from-file=AZURE_CLIENT_SECRET=/dev/stdin

unset AZURE_CLIENT_SECRET
```

The silent prompt keeps the secret out of shell history, and stdin keeps it
out of the `kubectl` process arguments. Run the `unset` command even if Secret
creation fails. This interactive path is for bootstrap only; use an external
secrets controller for a durable production workflow.

As soon as the Secret and all three selected keys exist, the kubelet starts the
existing manager Pods automatically. The chart maps their values to the Azure
SDK's `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, and `AZURE_CLIENT_SECRET`
environment variables; the Secret data keys themselves may use other names.

## Verify the control plane

```bash
kubectl rollout status \
  deployment/azure-workload-identity-operator-controller-manager \
  --namespace azure-workload-identity-operator-system

kubectl get pods \
  --namespace azure-workload-identity-operator-system

kubectl get pods \
  --namespace microsoft-azure-workload-identity-webhook-system

kubectl get certificate,issuer \
  --all-namespaces

kubectl get validatingwebhookconfiguration,mutatingwebhookconfiguration
```

The default production profile runs two manager replicas, two mutating-webhook replicas, and one `minAvailable: 1` PodDisruptionBudget for each workload.

## Understand the scope anchor

The chart writes the subscription ID, resource group, and location into the retained immutable `azure-workload-identity-operator-startup-config` ConfigMap. Every manager Pod mounts that ConfigMap and exits before constructing Kubernetes or Azure clients if its values are missing, malformed, or different from the command-line configuration.

Changing Azure scope is a migration, not an in-place upgrade. See [Ownership and lifecycle](../concepts/ownership-and-lifecycle.md).

## Next step

Follow the [quickstart](./quickstart.md) to publish an issuer and create a workload identity.
