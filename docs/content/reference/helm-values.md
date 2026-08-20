---
title: Helm values
description: Public configuration contract for the Azure Workload Identity Operator chart.
---

# Helm values

This page summarizes the maintained public values contract. The chart's `values.yaml` and `values.schema.json` remain authoritative for the exact release being installed.

## Azure scope and credentials

| Value | Default | Description |
| --- | --- | --- |
| `azure.tenantId` | `""` | Required Azure tenant for the bundled webhook and bootstrap context. |
| `azure.subscriptionId` | `""` | Required immutable installation subscription. |
| `azure.resourceGroupName` | `""` | Required immutable shared resource group. |
| `azure.location` | `""` | Required immutable Azure location. |
| `azure.credentials` | `{}` | Omit for another `DefaultAzureCredential` path. |
| `azure.credentials.secretRef.name` | — | External Secret in the release namespace. Required when `secretRef` is configured. |
| `azure.credentials.secretRef.keys.clientId` | — | Secret data key whose value becomes `AZURE_CLIENT_ID`. |
| `azure.credentials.secretRef.keys.tenantId` | — | Secret data key whose value becomes `AZURE_TENANT_ID`. |
| `azure.credentials.secretRef.keys.clientSecret` | — | Secret data key whose value becomes `AZURE_CLIENT_SECRET`. |

Subscription, resource group, and location are recorded in the retained startup anchor. They cannot change during an in-place installation lifecycle.

The Secret name and all three key selectors must be non-empty when
`azure.credentials.secretRef` is configured. The Secret remains external to
the Helm release.

## Manager

| Value | Default | Description |
| --- | --- | --- |
| `manager.replicas` | `2` | Controller manager replica count. |
| `manager.podDisruptionBudget.enabled` | `true` | Render the manager disruption budget. |
| `manager.podDisruptionBudget.minAvailable` | `1` | Minimum available manager Pods. |
| `manager.refreshIntervals.oidcIssuer` | `5m` | Issuer storage, key, and document refresh. |
| `manager.refreshIntervals.workloadIdentity` | `5m` | Base Azure and ServiceAccount drift refresh before stable jitter. |
| `manager.image.repository` | GHCR project image | Manager image repository. |
| `manager.image.digest` | `""` | Immutable digest; release charts set this. |
| `manager.image.tag` | `""` | Defaults to chart `appVersion` when digest is empty. |
| `manager.image.pullPolicy` | `IfNotPresent` | Kubernetes image pull policy. |
| `manager.imagePullSecrets` | `[]` | Pull Secrets for private mirrors. |
| `manager.extraEnv` | `[]` | Additional environment variables, primarily standard `OTEL_*` configuration. |
| `manager.extraVolumes` | `[]` | Additional Pod volumes. Fixed chart volumes cannot be replaced. |
| `manager.extraVolumeMounts` | `[]` | Additional manager mounts. Fixed mount paths cannot be replaced. |
| `manager.podLabels` | `{}` | Additional labels, including workload-identity mutation selection during manager auth migration. |
| `manager.podAnnotations` | mesh injection disabled | Additional Pod annotations. |
| `manager.nodeSelector` | `{}` | Manager scheduling selector. |
| `manager.tolerations` | `[]` | Manager tolerations. |
| `manager.affinity` | `{}` | Manager affinity. |
| `manager.topologySpreadConstraints` | zone and hostname soft spread | Default high-availability spread. |

Default manager resources:

```yaml
requests:
  cpu: 10m
  memory: 64Mi
limits:
  cpu: 500m
  memory: 128Mi
```

The default Pod and container security contexts enforce non-root execution, `RuntimeDefault` seccomp, no privilege escalation, a read-only root filesystem, and all capabilities dropped.

## Manager ServiceAccount

| Value | Default | Description |
| --- | --- | --- |
| `serviceAccount.create` | `true` | Create the manager ServiceAccount. |
| `serviceAccount.name` | `""` | Existing ServiceAccount name when creation is disabled. |
| `serviceAccount.annotations` | `{}` | Additional annotations, including Azure client ID during auth migration. |
| `serviceAccount.labels` | `{}` | Additional ServiceAccount labels. |

## CRDs and RBAC

| Value | Default | Description |
| --- | --- | --- |
| `crds.enabled` | `true` | Render the three upgradeable retained CRDs. |
| `rbac.helpers.enabled` | `false` | Render optional user-facing admin, editor, and viewer roles. |

Core controller RBAC always renders. Helper roles are not bound to users by the chart.

## Metrics and tracing

| Value | Default | Description |
| --- | --- | --- |
| `metrics.enabled` | `true` | Enable the manager metrics endpoint. |
| `metrics.port` | `8443` | Metrics Service port. |
| `metrics.secure` | `true` | Use authenticated and authorized HTTPS metrics. |
| `telemetry.tracing.enabled` | `false` | Enable in-process OpenTelemetry tracing configured by standard environment variables. |

## Validating webhook certificates

| Value | Default | Description |
| --- | --- | --- |
| `webhook.certificates.provider` | `certManager` | `certManager` or `existingSecret`. |
| `webhook.certificates.certManager.secretName` | `webhook-server-cert` | Secret populated by the chart Certificate. |
| `webhook.certificates.existingSecret.name` | `""` | Pre-created TLS Secret for an external PKI. |
| `webhook.certificates.existingSecret.caBundle` | `""` | PEM CA bundle embedded in the validating webhook configuration. |

## Bundled Azure Workload Identity webhook

| Value | Default | Description |
| --- | --- | --- |
| `azureWorkloadIdentityWebhook.enabled` | `true` | Install the bundled Microsoft mutating webhook. |
| `azureWorkloadIdentityWebhook.namespaceOverride` | `microsoft-azure-workload-identity-webhook-system` | Fixed cluster-singleton security boundary. Do not change. |
| `azureWorkloadIdentityWebhook.replicaCount` | `2` | Webhook replica count. |
| `azureWorkloadIdentityWebhook.image.repository` | Microsoft MCR webhook | Vendored upstream image repository. |
| `azureWorkloadIdentityWebhook.image.release` | `v1.6.0` | Vendored upstream release. |
| `azureWorkloadIdentityWebhook.image.digest` | pinned SHA-256 | Multi-architecture image identity. |
| `azureWorkloadIdentityWebhook.podDisruptionBudget.enabled` | `true` | Render webhook disruption budget. |
| `azureWorkloadIdentityWebhook.podDisruptionBudget.minAvailable` | `1` | Minimum available webhook Pods. |
| `azureWorkloadIdentityWebhook.azureEnvironment` | `AzurePublicCloud` | Cloud environment for the bundled mutating webhook only. The operator control plane currently supports Azure Public Cloud and does not derive its ARM, credential, or Storage endpoints from this value. |
| `azureWorkloadIdentityWebhook.logLevel` | `info` | Webhook logging level. |
| `azureWorkloadIdentityWebhook.metricsAddr` | `:8095` | Webhook metrics address. |
| `azureWorkloadIdentityWebhook.metricsBackend` | `prometheus` | Metrics backend. |
| `azureWorkloadIdentityWebhook.priorityClassName` | `system-cluster-critical` | Scheduling priority. |
| `azureWorkloadIdentityWebhook.mutatingWebhookNamespaceSelector` | `{}` | Optional namespace selector for admission. |
| `azureWorkloadIdentityWebhook.service.type` | `ClusterIP` | Webhook Service type. |

The parent chart owns the webhook tenant configuration and cert-manager certificate mode. The upstream certificate rotator is disabled.

`webhook.certificates.provider: existingSecret` affects only the operator's
validating webhook. The bundled mutating webhook continues to render its
cert-manager `Issuer` and `Certificate` while
`azureWorkloadIdentityWebhook.enabled` is `true`.

## Availability profiles

`values-production.yaml` repeats the chart defaults: two replicas and enabled disruption budgets. `values-single-replica.yaml` selects one replica and disables both budgets for local or disposable environments.

The single-replica profile is not a production availability claim.
