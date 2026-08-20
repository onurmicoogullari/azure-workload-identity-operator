---
title: Install with GitOps
description: Consume the immutable OCI chart through Argo CD, Kustomize Helm inflation, or reviewed rendered YAML.
---

# Install with GitOps

The published OCI Helm chart is the supported installation package. GitOps tools may consume it directly, inflate it through Kustomize, or apply reviewed YAML rendered from an exact release.

These examples define manifest sources; they do not prescribe app-of-apps, ApplicationSet, or another composition model.

## Shared requirements

- Pin an exact chart version.
- Keep Azure credentials out of Git and Helm values.
- Arrange an external Secret containing the client ID, tenant ID, and client secret, then configure its name and all three data keys explicitly.
- Let the deployment tool create the operator namespace.
- Review every cluster-scoped object when upgrading the chart.
- Keep automatic pruning disabled until retained-resource behavior is understood.
- Do not use force or replace semantics for this installation.

## Argo CD OCI source

An OCI source omits the `oci://` prefix:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: azure-workload-identity-operator
  namespace: openshift-gitops
spec:
  project: default
  source:
    repoURL: ghcr.io/onurmicoogullari/charts
    chart: azure-workload-identity-operator
    targetRevision: 0.1.0
    helm:
      valuesObject:
        azure:
          tenantId: '<tenant-id>'
          subscriptionId: '<subscription-id>'
          resourceGroupName: '<resource-group>'
          location: '<location>'
          credentials:
            secretRef:
              name: azure-workload-identity-operator-azure-credentials
              keys:
                clientId: AZURE_CLIENT_ID
                tenantId: AZURE_TENANT_ID
                clientSecret: AZURE_CLIENT_SECRET
  destination:
    server: https://kubernetes.default.svc
    namespace: azure-workload-identity-operator-system
  syncPolicy:
    syncOptions:
      - CreateNamespace=true
      - FailOnSharedResource=true
```

For private mirrors, configure a narrow read-only repository credential. Released charts already pin the validated manager image digest; leave `manager.image.digest` unset.

## Kustomize Helm inflation

Use this path when an Application must point to a Git directory. Argo CD must invoke Kustomize with `--enable-helm`.

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

Set both namespace fields. `helmCharts[].namespace` supplies `.Release.Namespace`; the Kustomization namespace transforms namespaced objects. Do not add `namePrefix` or `nameSuffix` to this cluster-singleton installation.

## Commit rendered YAML

When policy requires every object in Git, render from a downloaded exact chart package:

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

Regenerate and review the file for every chart or values change. Never render or commit the credential Secret.

## Scope and synchronization safety

The retained immutable startup ConfigMap has Argo CD `Prune=false,Delete=false` annotations and sync wave `-1`; the Deployment uses wave `0`. Those waves improve ordering within one Application but are not the safety boundary.

Runtime safety comes from the mounted scope anchor: Pods cannot start when the anchor is absent or different. This still works when Helm `lookup` cannot inspect the live cluster during GitOps rendering.

See [Upgrade and uninstall](./upgrades-and-uninstall.md) before enabling automated lifecycle operations.
