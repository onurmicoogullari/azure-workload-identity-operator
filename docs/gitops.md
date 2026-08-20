# GitOps installation

- Status: Supported
- Last reviewed: 2026-08-17

The published OCI Helm chart is the operator's installation package. GitOps
tools may consume it directly, inflate it through Kustomize, or apply YAML
rendered from an exact chart release.

These examples define manifest sources, not an Application architecture. They
do not require app-of-apps, ApplicationSet, or any other composition model.

## Before you install

Provide cert-manager and arrange for an externally managed Secret containing
the operator's Azure client ID, tenant ID, and client secret. Configure the
Secret name and all three data keys explicitly. Keep credentials out of Git and
Helm values.

Let the installation tool create the operator namespace: use Helm's
`--create-namespace` flag or Argo CD's `CreateNamespace=true` sync option. When
an ApplicationSet generates the operator Application, put that sync option in
the Application template.

Pin an exact chart version. If an Argo CD `AppProject` restricts cluster
resources, allow the cluster-scoped kinds rendered by that pinned chart and no
others. Review the rendered output when the chart version changes.

## Argo CD with the OCI chart

Use the [Application sample](../config/samples/gitops/application.yaml) when
Argo CD can read the chart registry directly. Replace the example Azure values,
chart version, destination, and project before use.

An OCI Helm source omits the `oci://` prefix:

```yaml
source:
  repoURL: ghcr.io/onurmicoogullari/charts
  chart: azure-workload-identity-operator
  targetRevision: 0.1.0
```

For a private mirror, configure a narrowly scoped read-only repository
credential in Argo CD. Released charts already pin the validated manager image
digest, so leave `manager.image.digest` unset.

The sample sets `CreateNamespace=true`, so the destination namespace need not
exist before the first sync. It also references a required credential Secret.
If that Secret or one of its three keys is not ready yet, Kubernetes creates
the Pod but starts none of its containers. The kubelet retries periodically and
starts the manager automatically after the Secret becomes available; this is
not a container restart or `CrashLoopBackOff`.

## Argo CD with Kustomize `helmCharts`

Use the [Kustomize sample](../config/samples/gitops/kustomize) when an
Application or ApplicationSet must point at a Git directory. Argo CD must run
Kustomize with `--enable-helm`.

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: azure-workload-identity-operator-system
helmCharts:
  - name: azure-workload-identity-operator
    repo: oci://ghcr.io/onurmicoogullari/charts
    version: 0.1.0
    releaseName: azure-workload-identity-operator
    namespace: azure-workload-identity-operator-system
    valuesFile: values.yaml
```

Set both namespace fields: `helmCharts[].namespace` supplies
`.Release.Namespace` during rendering, while the Kustomization namespace
transforms namespaced resources. Do not use `namePrefix` or `nameSuffix` for
this cluster-wide installation.

Unlike a native Argo OCI source, Kustomize invokes Helm while inflating the
chart. Configure Helm registry authentication in the repo-server for a private
mirror. Kustomize 5.8.1 cannot pin an OCI digest in `helmCharts`, so pin an
exact chart version and review the rendered output in CI.

## Committed rendered YAML

If policy requires every Kubernetes object in Git, render an exact chart
release during review:

```bash
helm pull \
  oci://ghcr.io/onurmicoogullari/charts/azure-workload-identity-operator \
  --version 0.1.0
helm template azure-workload-identity-operator \
  ./azure-workload-identity-operator-0.1.0.tgz \
  --namespace azure-workload-identity-operator-system \
  --include-crds \
  --values ./values.yaml \
  > ./azure-workload-identity-operator.yaml
```

Regenerate and review the file for every chart or values change. Do not render
or commit the credential Secret.

## Synchronization and scope safety

Set `CreateNamespace=true` and `FailOnSharedResource=true` for an Argo CD
Application. Keep automated pruning disabled until the platform has reviewed
all retained resources. Do not use `Force=true` or `Replace=true` for this
installation.

The chart records `azure.subscriptionId`, `azure.resourceGroupName`, and
`azure.location` in a retained immutable ConfigMap. Every manager Pod mounts
that anchor and exits before creating Azure or Kubernetes clients when the
configured scope is missing, malformed, or different.

The ConfigMap uses sync wave `-1`; the Deployment uses the default wave `0`.
Those waves order resources within the same operator Application. They do not
order independent Applications and do not imply an app-of-apps model. Runtime
safety does not depend on waves: a Pod cannot mount a missing anchor and cannot
start with a mismatched anchor.

The ConfigMap also carries `Prune=false,Delete=false`. Changing Azure scope is
an explicit migration, not an ordinary sync or rollback. See the
[chart guide](../dist/chart/README.md#azure-scope-boundary) for lifecycle
details and availability profiles.

## References

- [Argo CD Helm and OCI sources](https://argo-cd.readthedocs.io/en/stable/user-guide/helm/)
- [Argo CD Kustomize Helm enablement](https://argo-cd.readthedocs.io/en/stable/user-guide/kustomize/#kustomizing-helm-charts)
- [Argo CD sync waves](https://argo-cd.readthedocs.io/en/stable/user-guide/sync-waves/)
- [Argo CD sync options](https://argo-cd.readthedocs.io/en/stable/user-guide/sync-options/)
- [Kustomize Helm chart inflation](https://github.com/kubernetes-sigs/kustomize/blob/master/examples/chart.md)
