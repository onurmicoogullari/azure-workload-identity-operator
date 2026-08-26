---
title: Install the operator
description: Install the supported OCI Helm chart with a fixed Azure scope and externally managed credentials.
---

# Install the operator

Install the OCI Helm chart into a dedicated namespace. The chart installs the
operator, its three custom resource definitions (CRDs), validating webhooks,
the selected certificate integration, and the bundled Microsoft Azure
Workload Identity mutating webhook.

## Before you begin

You need:

- an OpenShift or Kubernetes cluster with Helm;
- a certificate provider for both admission webhooks: cert-manager, externally
  supplied TLS Secrets, or the OpenShift service CA operator;
- an Azure tenant, subscription, resource group name, and location;
- a Microsoft Entra Service Principal prepared using one of the [supported Azure permission models](../operations/permissions.md); and
- an external mechanism that creates a Kubernetes Secret containing the operator's Azure client ID, tenant ID, and client secret. You will configure the Secret name and all three data keys explicitly.

:::note Platform support
OpenShift 4.22.8 is the production acceptance target. Other Kubernetes distributions remain compatibility preview. See [Compatibility and support](../operations/compatibility.md).
:::

## Choose an admission certificate provider

Set `global.webhookCertificates.provider` once. The selection applies to both
admission webhook servers; they cannot accidentally use competing certificate
owners.

The webhooks still use separate certificates and Secrets because they have
different Service DNS names and live in different namespaces:

| Webhook | Purpose | Service | Serving Secret namespace |
| --- | --- | --- | --- |
| Operator webhook | Validates the operator custom resources | `azure-workload-identity-operator-webhook-service` | Helm release namespace |
| Azure Workload Identity webhook | Mutates opted-in Pods | `azure-wi-webhook-webhook-service` | `microsoft-azure-workload-identity-webhook-system` |

Choose one of these providers:

| Provider value | Choose it when | Result |
| --- | --- | --- |
| `certManager` | The cluster already uses cert-manager. This is the default and works on OpenShift and vanilla Kubernetes. | The chart creates one Issuer and Certificate per enabled webhook. cert-manager creates and rotates the Secrets and injects the CA bundles into the admission configurations. |
| `openShiftServiceCA` | The cluster is OpenShift and should use its built-in service CA operator. | The chart adds OpenShift serving-certificate and CA-injection annotations. The service CA operator creates and rotates both Secrets and injects both CA bundles. No cert-manager resources are created. |
| `selfManaged` | An external PKI or Secret controller owns the certificates. | The chart mounts the two named Secrets and embeds the supplied CA bundles. It does not create certificate resources, Secrets, or injection annotations. You own issuance, delivery, rotation, and CA rollover. |

The chart does not auto-detect OpenShift or cert-manager. The following short
guides show how to configure each choice. Put the configuration in the
environment values file passed to Helm, or use the equivalent Helm CLI options.

### Use cert-manager (default)

1. Install cert-manager and wait until its controller, webhook, and CA injector
   are Ready.
2. Keep the default, or make the choice explicit in your values file:

   ```yaml
   global:
     webhookCertificates:
       provider: certManager
   ```

3. Install the chart, then wait for both Certificates to become Ready:

   ```bash
   kubectl wait \
     certificate/azure-workload-identity-operator-serving-cert \
     --for=condition=Ready \
     --timeout=2m \
     --namespace azure-workload-identity-operator-system

   kubectl wait certificate/azure-wi-webhook-serving-cert \
     --for=condition=Ready \
     --timeout=2m \
     --namespace microsoft-azure-workload-identity-webhook-system
   ```

The default Secret names are `webhook-server-cert` in the release namespace
and `azure-wi-webhook-server-cert` in the bundled webhook namespace.

### Use the OpenShift service CA operator

1. Confirm the OpenShift service CA cluster operator is available:

   ```bash
   oc wait clusteroperator/service-ca \
     --for=condition=Available=True \
     --timeout=5m
   ```

2. Select the provider in your values file:

   ```yaml
   global:
     webhookCertificates:
       provider: openShiftServiceCA
   ```

3. Install the chart. You do not need cert-manager for this mode.
4. Verify that OpenShift created both serving Secrets and injected both CA
   bundles:

   ```bash
   oc get secret webhook-server-cert-openshift \
     --namespace azure-workload-identity-operator-system

   oc get secret azure-wi-webhook-server-cert-openshift \
     --namespace microsoft-azure-workload-identity-webhook-system

   oc get validatingwebhookconfiguration \
     azure-workload-identity-operator-validating-webhook-configuration \
     -o jsonpath='{.webhooks[0].clientConfig.caBundle}{"\n"}'

   oc get mutatingwebhookconfiguration \
     azure-wi-webhook-mutating-webhook-configuration \
     -o jsonpath='{.webhooks[0].clientConfig.caBundle}{"\n"}'
   ```

Each JSONPath command must print a non-empty base64 value. This provider is
OpenShift-specific; on vanilla Kubernetes, no controller responds to its
annotations. See the official [OpenShift 4.22 service serving certificate documentation](https://docs.redhat.com/en/documentation/openshift_container_platform/4.22/html/security_and_compliance/configuring-certificates#add-service-serving).

### Use self-managed certificates

`selfManaged` means externally managed; the certificates may be issued by an
enterprise CA, a Secret controller, or another PKI workflow. They do not have
to be self-signed.

1. Issue one certificate for each Service. Include these DNS names:

   - operator certificate:
     `azure-workload-identity-operator-webhook-service.<release-namespace>.svc`
     and
     `azure-workload-identity-operator-webhook-service.<release-namespace>.svc.cluster.local`;
   - bundled webhook certificate:
     `azure-wi-webhook-webhook-service.microsoft-azure-workload-identity-webhook-system.svc`
     and
     `azure-wi-webhook-webhook-service.microsoft-azure-workload-identity-webhook-system.svc.cluster.local`.

2. Add the Secret names and the PEM CA that verifies each serving certificate
   to the Helm install command. Use these options instead of a provider values
   file; `--set-file` preserves the PEM content:

   ```bash
   --set-string global.webhookCertificates.provider=selfManaged \
   --set-string global.webhookCertificates.selfManaged.operator.secretName=operator-webhook-tls \
   --set-file global.webhookCertificates.selfManaged.operator.caBundle=./operator-ca.pem \
   --set-string global.webhookCertificates.selfManaged.azureWorkloadIdentity.secretName=azure-wi-webhook-tls \
   --set-file global.webhookCertificates.selfManaged.azureWorkloadIdentity.caBundle=./azure-wi-ca.pem
   ```

3. On a first install, omit Helm's `--wait` flag so the chart can create both
   namespaces. Then create or externally populate both TLS Secrets:

   ```bash
   kubectl create secret tls operator-webhook-tls \
     --cert=./operator-tls.crt \
     --key=./operator-tls.key \
     --namespace azure-workload-identity-operator-system

   kubectl create secret tls azure-wi-webhook-tls \
     --cert=./azure-wi-tls.crt \
     --key=./azure-wi-tls.key \
     --namespace microsoft-azure-workload-identity-webhook-system
   ```

   The Secrets may be created later by an external controller, but the webhook
   Pods cannot become Ready until each Secret contains `tls.crt` and `tls.key`.
   The CA bundles are required immediately because Helm embeds them in the
   admission configurations while rendering the release.

4. Verify the Secrets and both webhook Deployments:

   ```bash
   kubectl get secret operator-webhook-tls \
     --namespace azure-workload-identity-operator-system

   kubectl get secret azure-wi-webhook-tls \
     --namespace microsoft-azure-workload-identity-webhook-system

   kubectl rollout status \
     deployment/azure-workload-identity-operator-controller-manager \
     --namespace azure-workload-identity-operator-system

   kubectl rollout status deployment/azure-wi-webhook-controller-manager \
     --namespace microsoft-azure-workload-identity-webhook-system
   ```

For certificate renewal under the same CA, replace the corresponding Secret;
the webhook reloads the mounted key pair. For a CA change, follow the
[safe CA rollover procedure](../guides/upgrades-and-uninstall.md#rotate-a-self-managed-webhook-ca).
See [Helm values](../reference/helm-values.md#webhook-certificate-providers)
for every setting and default.

## Prepare the Azure Service Principal

Create a Service Principal for the operator through your organization's normal
Azure provisioning process. The operator does not create Azure role
assignments and cannot grant data access to itself.

Choose one bootstrap model:

| Model | Resources created before installation | Service Principal access |
| --- | --- | --- |
| **Pre-created resource group (recommended)** | A dedicated resource group | `Contributor` and `Storage Blob Data Contributor` on the dedicated resource group |
| **Operator-created resource group** | None | `Contributor` and `Storage Blob Data Contributor` at subscription scope for an unattended bootstrap |

The recommended model limits both roles to one dedicated resource group. The
operator creates and manages the Storage account, blob container, managed
identities, and federated credentials inside that group.

The Service Principal does not need `Owner`, `User Access Administrator`, or
permission to create role assignments in either model. Record its application
(client) ID, tenant ID, and client secret for the credential Secret. See
[Permissions](../operations/permissions.md) for detailed operations and a
staged alternative to subscription-wide blob data access.

## Install the published chart

Install the release before creating the credential Secret. Helm creates the
operator namespace and all release resources. OCI charts do not use
`helm repo add`. Pass the environment values file containing your provider
choice with `--values`, or append the self-managed `--set-string` and
`--set-file` options shown above:

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

kubectl get validatingwebhookconfiguration,mutatingwebhookconfiguration
```

For cert-manager mode, also verify both Certificate and Issuer resources. For
OpenShift service CA mode, verify the two configured serving Secrets and the
`service.beta.openshift.io/inject-cabundle` annotations instead.

The default production profile runs two manager replicas, two mutating-webhook replicas, and one `minAvailable: 1` PodDisruptionBudget for each workload.

## Understand the scope anchor

The chart writes the subscription ID, resource group, and location into the retained immutable `azure-workload-identity-operator-startup-config` ConfigMap. Every manager Pod mounts that ConfigMap and exits before constructing Kubernetes or Azure clients if its values are missing, malformed, or different from the command-line configuration.

Changing Azure scope is a migration, not an in-place upgrade. See [Ownership and lifecycle](../concepts/ownership-and-lifecycle.md).

:::warning Plan for an OpenShift API-server rollout
After installation, creating `OIDCIssuer/default` with
`spec.openShift.updateServiceAccountIssuer: true` changes
`Authentication/cluster.spec.serviceAccountIssuer` when its current value does
not already match the published issuer. OpenShift then rolls out a new revision
of the Kubernetes API-server Pods, which can briefly interrupt API requests and
existing `oc` sessions. Schedule this change appropriately and wait for the
`kube-apiserver`, `authentication`, and `openshift-apiserver` ClusterOperators
to settle.

This rollout replaces control-plane Pods; it does not reboot nodes or restart
application workloads. No rollout occurs when the configured issuer already
matches. See [Manage the OIDC issuer](../guides/oidc-issuer.md) for the complete
handoff behavior.
:::

## Next step

Continue with [Configure and test workload identity](./quickstart.md) to publish
an issuer, create a workload identity, and verify Azure token exchange from a
Pod.
