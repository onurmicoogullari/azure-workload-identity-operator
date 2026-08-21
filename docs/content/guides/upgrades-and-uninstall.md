---
title: Upgrade, roll back, and uninstall
description: Preserve CRDs, custom resources, scope identity, and external state through chart lifecycle operations.
---

# Upgrade, roll back, and uninstall

The chart is designed so a Helm release can be upgraded, removed, and reinstalled without silently deleting custom resources or changing the Azure scope they govern.

## Upgrade

1. Read the release notes and inspect changed chart values.
2. Pull and render the exact target chart.
3. Review CRD schema changes and all cluster-scoped resources.
4. Confirm the configured Azure subscription, resource group, and location exactly match the retained startup ConfigMap.
5. Upgrade without replacing CRDs or the anchor.
6. Wait for manager and webhook rollouts.
7. Confirm webhook admission and reconcile one issuer and workload identity.

```bash
helm upgrade azure-workload-identity-operator \
  oci://ghcr.io/onurmicoogullari/charts/azure-workload-identity-operator \
  --version '<new-version>' \
  --namespace azure-workload-identity-operator-system \
  --reuse-values
```

Prefer an explicit environment values file over `--reuse-values` in controlled deployment pipelines; the example emphasizes that the installation identity must remain the same.

CRDs live in chart templates rather than Helm's one-time `crds/` directory, so schema changes are applied during upgrade. They carry `helm.sh/resource-policy: keep`.

### Change a webhook certificate provider

Treat a provider change as an availability-sensitive migration because both
admission configurations fail closed.

When the old and new providers use different CAs, matching admission requests
can fail during the rollout. Pause affected custom-resource changes and
opted-in Pod creation until both webhook Deployments and CA bundles are
verified.

1. Change the shared `global.webhookCertificates.provider` value; the chart
   applies it to both webhooks.
2. Before selecting `selfManaged` on an existing release, create the
   operator TLS Secret in the release namespace and the bundled-webhook TLS Secret in
   `microsoft-azure-workload-identity-webhook-system`. Confirm each certificate
   covers its Service DNS name and each configured PEM CA verifies it.
3. Before selecting `certManager`, install cert-manager and wait until it is
   Ready. Before selecting `openShiftServiceCA`, confirm the OpenShift service
   CA operator is available.
4. Render the upgrade and verify each webhook has exactly one owner: a
   cert-manager injection annotation, an embedded self-managed CA bundle,
   or an OpenShift injection annotation.
5. Upgrade during a maintenance window, wait for both Deployments, and prove
   API-server admission through both webhook configurations.

Helm 4 uses server-side apply. When leaving a controller-injected CA mode, the
old cert-manager or OpenShift injector can still own
`webhooks[].clientConfig.caBundle` at the first upgrade. After verifying the
rendered handoff, add `--force-conflicts` to that provider-switching upgrade so
Helm deliberately takes ownership of the embedded CA or removes the old
injected field. Do not make this a blanket flag for routine upgrades. Helm 3's
client-side update path does not use server-side field ownership.

The default OpenShift Secret names differ from the cert-manager Secret names.
This prevents the old and new controllers from racing over one Secret during a
provider switch. Do not override them to the same name during a migration.

Rollback has the same prerequisites as a forward switch. In particular,
install cert-manager and wait for it to become Ready before rolling back to a
revision that selects `certManager`; retain externally supplied Secrets until
a rollback window has closed.

### Rotate a self-managed webhook CA

Both admission configurations use `failurePolicy: Fail`. Do not replace a
serving certificate and its trusted CA in a single uncoordinated step: the API
server can reject matching requests while one side still uses the old CA.

Use this three-phase rollover independently for the operator webhook, the
bundled Azure Workload Identity webhook, or both:

1. **Trust both CAs.** Concatenate the old and new PEM CA certificates, then
   upgrade the release with the combined bundle in the corresponding
   `global.webhookCertificates.selfManaged.<webhook>.caBundle` value. Verify
   that the admission configuration contains the new bundle before continuing.
2. **Rotate the serving Secret.** Replace `tls.crt` and `tls.key` in the
   corresponding Secret with a certificate issued by the new CA. The webhook
   reloads the mounted key pair without a Pod restart. Verify the Deployment is
   Ready and exercise the affected admission path.
3. **Remove the old CA.** Upgrade the release again with only the new CA in the
   bundle, then recheck admission.

Renewing a serving certificate under the same CA only requires step 2. If both
webhooks share a CA, keep the old CA in both admission bundles until both
serving Secrets use certificates issued by the new CA.

## Roll back

Use a chart version that supports every persisted CRD field currently in use. A Helm rollback can restore controller and webhook resources but does not downgrade stored objects automatically.

```bash
helm history azure-workload-identity-operator \
  --namespace azure-workload-identity-operator-system

helm rollback azure-workload-identity-operator '<revision>' \
  --namespace azure-workload-identity-operator-system \
  --wait
```

The scope anchor intentionally prevents a rollback from changing Azure scope.

## Routine uninstall and reinstall

An ordinary uninstall retains:

- all three CRDs and therefore all custom resources;
- the immutable startup scope ConfigMap;
- the fixed `microsoft-azure-workload-identity-webhook-system` namespace; and
- every externally owned credential Secret and retained Azure resource.

It removes the manager, webhook configurations, certificate resources, and bundled webhook workloads.

```bash
helm uninstall azure-workload-identity-operator \
  --namespace azure-workload-identity-operator-system
```

A same-name, same-namespace reinstall with the exact same Azure scope is the supported recovery path. Do not delete custom resources merely to reinstall the chart.

## Release name or namespace migration

Stable cluster-scoped resources enforce one Helm release per cluster. A release identity change requires an explicit ownership migration after the old release is absent.

For a namespace move:

1. create the new namespace;
2. copy the retained startup ConfigMap with identical `data`;
3. transfer Helm ownership annotations and labels on the CRDs, fixed webhook namespace, and copied ConfigMap; and
4. immediately install the new release with the same scope.

After the old release is absent and the retained startup ConfigMap exists in
the target namespace, transfer ownership to the new release identity:

```bash
new_release=azure-workload-identity-operator
new_namespace=azure-workload-identity-operator-system

kubectl annotate --overwrite \
  crd/oidcissuers.workloadidentity.azure.micosolutions.se \
  crd/workloadidentities.workloadidentity.azure.micosolutions.se \
  crd/workloadidentityrecoveries.workloadidentity.azure.micosolutions.se \
  namespace/microsoft-azure-workload-identity-webhook-system \
  meta.helm.sh/release-name="$new_release" \
  meta.helm.sh/release-namespace="$new_namespace"

kubectl label --overwrite \
  crd/oidcissuers.workloadidentity.azure.micosolutions.se \
  crd/workloadidentities.workloadidentity.azure.micosolutions.se \
  crd/workloadidentityrecoveries.workloadidentity.azure.micosolutions.se \
  namespace/microsoft-azure-workload-identity-webhook-system \
  app.kubernetes.io/managed-by=Helm

kubectl annotate --overwrite \
  --namespace "$new_namespace" \
  configmap/azure-workload-identity-operator-startup-config \
  meta.helm.sh/release-name="$new_release" \
  meta.helm.sh/release-namespace="$new_namespace"

kubectl label --overwrite \
  --namespace "$new_namespace" \
  configmap/azure-workload-identity-operator-startup-config \
  app.kubernetes.io/managed-by=Helm
```

Install the new release immediately with the same Azure scope, then verify all
retained resources before making any separate migration.

Do not combine a Helm ownership move with an Azure scope migration.

## Permanent decommission

:::danger Destructive operation
Deleting a CRD deletes every Kubernetes custom resource stored under it and bypasses its controller finalizers. Never begin with CRD deletion.
:::

1. If OpenShift issuer management is enabled, complete the [issuer handoff](./oidc-issuer.md#hand-off-the-openshift-issuer).
2. Delete `WorkloadIdentityRecovery` audit objects that are complete and no longer required.
3. Delete every `WorkloadIdentity` and wait for finalizers and requested Azure cleanup.
4. Delete `OIDCIssuer/default` and wait for its deletion guards and cleanup.
5. Verify no custom resources remain.
6. Uninstall the Helm release.
7. Explicitly delete the retained startup ConfigMap and three CRDs.
8. After proving no release owns it, delete the fixed bundled-webhook namespace.

Choose each custom resource's `deletionPolicy` before starting. Changing it after deletion begins may not be admitted or observed as intended.
