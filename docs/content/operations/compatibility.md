---
title: Compatibility and support
description: Understand the production acceptance target, preview platforms, architectures, and required dependencies.
---

# Compatibility and support

The operator is OpenShift-first. Compatibility claims describe tested acceptance targets, not every platform on which the controller may happen to run.

## Current matrix

| Area | Status | Evidence |
| --- | --- | --- |
| OpenShift 4.22.8 | Required production acceptance target | Release-candidate smoke testing must cover the complete Azure path under target cluster policies. |
| Kind / upstream Kubernetes | Compatibility preview | Chart lifecycle, validating admission, and controller integration are tested; the complete Azure issuer and token-exchange path is not yet covered end to end. |
| Azure Public Cloud | Supported Azure environment | Operator ARM, identity, token, and `blob.core.windows.net` Storage endpoints are configured and tested for the public cloud. |
| Azure sovereign clouds | Not supported | Operator clients and issuer URLs are not currently configurable as one sovereign-cloud environment. |
| Linux `amd64` | Supported image platform | Built, vulnerability-scanned, and published in the release image index. |
| Linux `arm64` | Supported image platform | Built, vulnerability-scanned, and published in the release image index. |

OpenShift 4.22.8 is a required target, not a declared minimum version. Older OpenShift releases are not automatically excluded, but they do not satisfy the current release acceptance requirement.

:::warning Azure cloud boundary
The operator control plane currently supports Azure Public Cloud only. The
`azureWorkloadIdentityWebhook.azureEnvironment` Helm value configures the
bundled mutating webhook; it does not change the operator's Azure Resource
Manager, Microsoft Entra ID, credential, or Storage endpoints. Setting that
value to a sovereign cloud does not make the complete operator path compatible
with that cloud.
:::

## Required cluster dependencies

- Helm for installation or a GitOps tool capable of rendering the chart.
- cert-manager before chart installation.
- Admission registration and functioning API aggregation/webhook connectivity.
- Network access from the manager to the Kubernetes API, Microsoft Entra ID, Azure Resource Manager, and Azure Storage.
- Network access from applications to Microsoft Entra ID and their target Azure services.

The chart bundles Microsoft Azure Workload Identity webhook v1.6.0 by multi-architecture digest. Disable it only when the cluster already owns a compatible installation and its operational responsibility is explicit.

## OpenShift security context

Both operator workloads omit fixed UID and GID values so the OpenShift restricted security context constraint can assign namespace-scoped identities. They use:

- `runAsNonRoot: true`;
- `allowPrivilegeEscalation: false`;
- a read-only root filesystem;
- all Linux capabilities dropped; and
- `RuntimeDefault` seccomp.

Istio and Linkerd sidecar injection are disabled by default. Admission webhooks and identity control planes should not depend on a service-mesh data plane becoming ready.

## Production acceptance

Before using a release on a production cluster, validate the exact chart and image digest under the environment's:

- security context constraints or Pod Security admission;
- proxy and private trust configuration;
- DNS and egress policies;
- registry mirror policy;
- cert-manager version;
- optional OpenTelemetry path; and
- GitOps rendering and pruning behavior.

The release process documents the required evidence in [Releasing](../contributing/releasing.md).
