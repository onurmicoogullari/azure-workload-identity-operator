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
