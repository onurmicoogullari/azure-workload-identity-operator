# Azure Workload Identity Operator Helm Chart

This first-party chart installs the operator, its three CRDs, controller RBAC,
real validating webhooks, cert-manager-backed serving certificates, and the
Microsoft Azure Workload Identity mutating webhook. The bundled mutating
webhook is pinned to upstream `v1.6.0` by multi-architecture image digest.

## Prerequisites

- Kubernetes or OpenShift with Helm
- cert-manager installed before this chart
- one target namespace for the operator; `azure-workload-identity-operator-system`
  is the documented canonical choice
- an Azure authentication method for the operator

The chart intentionally does not install cert-manager. Namespaced self-signed
Issuers are used only for the two internal API-server webhook endpoints and can
coexist with public ACME `ClusterIssuer` resources.

## Platform support

OpenShift is the current production-verified platform. Chart lifecycle and
admission are also exercised on Kind, but the full Azure issuer and token
exchange flow does not yet have vanilla Kubernetes e2e coverage. Until that
planned follow-up lands, other Kubernetes distributions are compatibility
preview rather than a production support claim.

## Service Principal bootstrap

Create a Secret in the release namespace. The Secret is not part of the Helm
release and its values must never be placed in a values file:

```bash
kubectl create namespace azure-workload-identity-operator-system
kubectl create secret generic azure-workload-identity-operator-azure-credentials \
  --namespace azure-workload-identity-operator-system \
  --from-literal=AZURE_CLIENT_ID='<client-id>' \
  --from-literal=AZURE_TENANT_ID='<tenant-id>' \
  --from-literal=AZURE_CLIENT_SECRET='<client-secret>'
```

Install from source:

```bash
helm dependency build --skip-refresh ./dist/chart
helm install azure-workload-identity-operator ./dist/chart \
  --namespace azure-workload-identity-operator-system \
  --set-string azure.tenantId='<tenant-id>' \
  --set-string azure.subscriptionId='<subscription-id>' \
  --set-string azure.resourceGroupName='<resource-group>' \
  --set-string azure.location='<location>' \
  --set-string azure.credentials.existingSecret=azure-workload-identity-operator-azure-credentials
```

Published releases are available as OCI charts:

```bash
helm install azure-workload-identity-operator \
  oci://ghcr.io/onurmicoogullari/charts/azure-workload-identity-operator \
  --version '<version>' \
  --namespace azure-workload-identity-operator-system \
  --create-namespace \
  --set-string azure.tenantId='<tenant-id>' \
  --set-string azure.subscriptionId='<subscription-id>' \
  --set-string azure.resourceGroupName='<resource-group>' \
  --set-string azure.location='<location>' \
  --set-string azure.credentials.existingSecret=azure-workload-identity-operator-azure-credentials
```

`azure.subscriptionId`, `azure.resourceGroupName`, and `azure.location` are
installation identity. The chart records them and rejects in-place Helm
upgrades that change them. Move to a deliberately planned new installation to
change Azure scope.

## Authentication evolution

`azure.credentials.existingSecret` is the initial bootstrap path. The Secret
must contain the three Azure SDK environment keys shown above. To move
the operator itself to an already-established workload or managed identity:

1. establish and verify that identity outside this release;
2. for workload identity, set the ServiceAccount client-ID annotation through
   `serviceAccount.annotations` and set
   `manager.podLabels.azure.workload.identity/use: "true"` so the bundled
   mutating webhook injects the projected token configuration;
3. grant the identity the documented Azure permissions;
4. upgrade with `azure.credentials.existingSecret=""`;
5. remove the old Secret only after the operator is Ready and reconciliation is
   verified.

The operator uses Azure SDK `DefaultAzureCredential`; the chart does not expose
separate authentication-mode switches or secret-valued Helm settings.

## Signing key Secret access

The manager ClusterRole grants cluster-wide `get` access to Secrets because
the cluster-scoped `OIDCIssuer` API supports active and retiring signing-key
references with arbitrary names and namespaces. It does not grant Secret
`list` or `watch`. The operator performs direct named API reads during
reconciliation, so changing a Secret reference is observed immediately and
updating data in an existing Secret is observed by periodic OIDCIssuer refresh
(five minutes by default).

Treat permission to create or modify `OIDCIssuer` as a cluster-administrator
capability: such a user can direct this cluster-trusted controller to retrieve
a Secret. Optional user-facing RBAC helper roles remain disabled by default.

## Namespace, naming, and scheduling

Operator resources use `.Release.Namespace`; the chart does not create that
namespace. The canonical operator namespace is a convention for predictable
operations, not a hard-coded destination. A namespace move is a migration
because cluster-scoped resources use stable, release-independent names.

The bundled Azure Workload Identity webhook is a cluster singleton in the
fixed `microsoft-azure-workload-identity-webhook-system` namespace. The chart creates and
owns that reserved namespace, and retains the Namespace object on uninstall so
an uninstall/reinstall cannot silently transfer its security boundary. Do not
place unrelated workloads there or change
`azureWorkloadIdentityWebhook.namespaceOverride`.

Those stable names deliberately enforce one Helm release per cluster. Two
controller replicas and two bundled webhook replicas are the defaults. Soft
topology spread constraints prefer different zones and nodes without making a
single-node or single-zone cluster unschedulable.

Both workloads omit fixed UID/GID settings so OpenShift restricted SCC can
assign namespace-scoped IDs. Istio and Linkerd sidecar injection are disabled
by default because admission webhooks and operator control planes should not
depend on a service-mesh data plane becoming ready.

## Certificates

The default `webhook.certificates.provider=certManager` creates a namespaced
self-signed `Issuer` and a `Certificate`; cert-manager writes `tls.crt` and
`tls.key` to the configured Secret and injects the CA into the validating
webhook configuration. The bundled mutating webhook always uses an equivalent
Issuer and Certificate in `microsoft-azure-workload-identity-webhook-system`.

The vendored webhook's upstream certificate rotator is deliberately disabled.
cert-manager owns renewal and CA injection, so the webhook ServiceAccount can
read ServiceAccounts but cannot read Secrets or update
MutatingWebhookConfigurations. Both admission configurations use
`failurePolicy: Fail`; certificate readiness is therefore part of installation
readiness, not an optional best-effort path.

For externally managed internal PKI, set the provider to `existingSecret`,
provide a pre-created TLS Secret, and provide its PEM CA certificate as the
non-secret `caBundle` value. No cert-manager resources render in that mode.

## CRDs, upgrades, and uninstall

CRDs are rendered as upgradeable Helm templates and carry
`helm.sh/resource-policy: keep`. Helm upgrades therefore apply schema changes,
while an ordinary uninstall retains the CRDs and every custom resource. A
same-name, same-namespace reinstall adopts those retained resources and is the
normal rollback/recovery path. Do not delete custom resources merely to
reinstall the chart. The `azure-workload-identity-operator-startup-config`
ConfigMap is also retained as the Azure-scope identity anchor, so reinstall
cannot silently change subscription, resource group, or location for retained
custom resources.

The Azure credential Secret is always externally owned and retained. Other
chart resources, including webhook configurations, certificate resources, and
the bundled mutating webhook, are removed by Helm uninstall. The fixed webhook
Namespace and Azure-scope identity ConfigMap are retained but their other
namespaced chart resources are removed.

Changing the Helm release name or release namespace is an explicit ownership
migration. After uninstalling the old release and before installing the new
one, transfer Helm ownership annotations on the three retained CRDs and the
fixed webhook Namespace to the new release name and namespace. Do this only
after proving the old release is absent; the chart intentionally rejects an
implicit takeover.

For a release-namespace move, first create the new operator namespace and copy
the retained startup ConfigMap into it with exactly the same `data`. Do not
change those fields as part of an ownership move.

For example, after checking `helm list -A` and choosing the new identity:

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

Install the new release immediately with the same Azure scope and verify all
retained resources before making any separate scope migration.

For a permanent, destructive decommission:

1. while the operator is running, delete each custom resource in dependency
   order and wait for all finalizers and requested Azure cleanup to finish;
2. verify no `OIDCIssuer`, `WorkloadIdentity`, or
   `WorkloadIdentityRecovery` objects remain;
3. uninstall the Helm release;
4. explicitly delete the retained startup ConfigMap and three retained CRDs;
   and
5. after verifying no release owns it, delete
   `microsoft-azure-workload-identity-webhook-system`.

Deleting a CRD deletes every custom resource stored under it and bypasses the
operator's Azure cleanup. It is never part of routine uninstall or reinstall.

## Chart ownership

Kubebuilder's Helm plugin scaffolded the initial supported directory structure
and recorded the plugin in `PROJECT`. From that point, `dist/chart` is
maintained source code. Do not routinely force-regenerate it: a plugin refresh
can overwrite the public values contract and handwritten templates.

`dist/install.yaml` is a generated, ignored plugin input rather than a second
distribution artifact. If a deliberate Kubebuilder plugin migration requires
a fresh scaffold, generate that input with `make build-installer` immediately
before running the plugin, review the resulting chart as a migration, and then
restore the maintained public contract.

The vendored Microsoft subchart source lives under
`dist/vendor/workload-identity-webhook`. Its README records the upstream source,
license, security-context delta, image digest, and update procedure. Run
`make helm-dependency` after a deliberate vendor update, and run
`make helm-lint` for the supported rendering matrix.
