---
title: Testing
description: Run unit, envtest, chart integration, Kind end-to-end, and OpenShift CRC/Azure verification.
---

# Testing

Use the smallest test that exercises the change, then run the broader gates required by its risk and affected boundary.

## Unit and envtest

```bash
make test
```

The controller and webhook suites use Ginkgo and Gomega. Envtest runs a real Kubernetes API server and etcd without starting a complete cluster.

## Lint and generation checks

```bash
make lint
make manifests generate
git status --short
```

Generated output must be committed when its source markers or API types changed.

## Helm validation

```bash
make helm-lint
make test-chart-integration
```

The chart tests cover rendering contracts, values combinations, certificates, RBAC, lifecycle, and a Kind-installed release.

## Kind end-to-end tests

Use an isolated Kind cluster, never a shared development or production context:

```bash
make test-e2e-kind
```

Kind covers the chart and Kubernetes control-plane behavior. It does not currently prove the full Azure issuer and token-exchange path.

## OpenShift CRC/Azure test

The packaged end-to-end test creates real Azure resources and changes the disposable OpenShift service-account issuer. It must begin from a fresh CRC cluster:

```bash
crc delete -f
crc setup
crc start
```

Keep the shell that started CRC open so the VM session remains alive. In a separate shell:

```bash
eval $(crc oc-env)
oc login -u kubeadmin -p '<password-from-crc-start>' \
  https://api.crc.testing:6443
oc wait clusterversion/version \
  --for='condition=Available=True' \
  --timeout=10m
oc get clusteroperators
```

All ClusterOperators must be Available, not Progressing, and not Degraded before the test starts.

Run from the repository root:

```bash
make test-e2e-crc
```

The test installs the packaged Helm release, bundles the Azure Workload Identity webhook, publishes OIDC documents, changes OpenShift authentication, builds a test workload, validates Key Vault access, exercises recovery and deletion guards, and verifies cleanup.

It creates real Azure resources and may create an ephemeral operator Service Principal. Read `test/e2e/openshift/README.md` before running it.

## Documentation

```bash
cd docs
npm ci
npm run lint
npm run typecheck
npm run build
```

The production build treats broken internal links as errors.
