# azure-workload-identity-operator

A Kubernetes operator for managing Azure workload identity infrastructure and
the cluster's OpenID Connect issuer integration.

## Support status

The operator is currently **OpenShift-first**, with OpenShift 4.22.8 as the
required production acceptance target. Chart lifecycle and admission are also
tested on Kind, but the complete Azure issuer and token-exchange path is not yet
covered by vanilla Kubernetes end-to-end tests. Other Kubernetes distributions
are therefore compatibility preview rather than a production support claim.

## Install

The supported release package is the OCI Helm chart. It installs the operator,
CRDs, validating webhooks, certificate resources, and the bundled Azure
Workload Identity mutating webhook.

Before installation, provide:

- cert-manager;
- an external mechanism that creates a Secret containing the operator's Azure
  client ID, tenant ID, and client secret; and
- the cluster's Azure subscription ID, resource group, and location.

Let Helm or the GitOps tool create the operator namespace during installation.

Keep credentials out of Git and Helm values. See the
[chart guide](dist/chart/README.md) for the complete installation contract,
availability profiles, authentication options, upgrades, and uninstall.

### Helm

OCI charts do not use `helm repo add`. Install a public chart directly from its
`oci://` reference; use `helm registry login` first for a private mirror.

```bash
helm upgrade --install azure-workload-identity-operator \
  oci://ghcr.io/onurmicoogullari/charts/azure-workload-identity-operator \
  --version 0.1.0 \
  --namespace azure-workload-identity-operator-system \
  --create-namespace \
  --set-string azure.tenantId='<tenant-id>' \
  --set-string azure.subscriptionId='<subscription-id>' \
  --set-string azure.resourceGroupName='<resource-group>' \
  --set-string azure.location='<location>' \
  --set-string azure.credentials.secretRef.name='<secret-name>' \
  --set-string azure.credentials.secretRef.keys.clientId='AZURE_CLIENT_ID' \
  --set-string azure.credentials.secretRef.keys.tenantId='AZURE_TENANT_ID' \
  --set-string azure.credentials.secretRef.keys.clientSecret='AZURE_CLIENT_SECRET'
```

If the external mechanism has not created the Secret yet, the manager Pods wait
without starting. They start automatically after the Secret becomes available.

### GitOps

GitOps tools can consume the OCI chart directly, inflate it through Kustomize
`helmCharts`, or apply reviewed YAML rendered from an exact chart release. See
the [GitOps guide](docs/gitops.md) for minimal Argo CD and Kustomize examples
and the shared safety requirements. Application composition remains a platform
concern.

## Azure scope boundary

One operator installation owns one Azure subscription, resource group, and
location. The chart records that identity in a retained immutable ConfigMap,
and every manager Pod verifies the mounted values before creating Kubernetes or
Azure clients. Startup fails closed when the anchor is missing, malformed, or
different from the configured scope.

This runtime boundary also protects GitOps rendering, where Helm `lookup`
cannot inspect the live cluster. Changing Azure scope requires an explicit
migration; see the [chart guide](dist/chart/README.md#azure-scope-boundary).

## E2E tests

The local OpenShift/CRC end-to-end test lives in `test/e2e/openshift/`:

```bash
make test-e2e-crc
```

It creates temporary Azure resources and, by default, a short-lived Entra
application and Service Principal for the in-cluster operator. See
[`test/README.md`](test/README.md) and the
[OpenShift test guide](test/e2e/openshift/README.md) for prerequisites,
behavior, and cleanup.

## Operations

- [Helm chart](dist/chart/README.md)
- [GitOps installation](docs/gitops.md)
- [Azure and Kubernetes permissions](docs/permissions.md)
- [Telemetry and OpenTelemetry tracing](docs/telemetry.md)
- [Controlled workload identity recovery](docs/recovery.md)
- [Release process](docs/releasing.md)
