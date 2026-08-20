# Azure Workload Identity Operator Helm chart

This directory contains the maintained source for the first-party OCI Helm
chart. This README is for contributors who maintain and validate that source.

## Maintainer workflow

Kubebuilder's Helm plugin scaffolded this directory, but it is now maintained
source code. Do not routinely force-regenerate it. A plugin refresh can
overwrite the public values contract and handwritten templates.

If a deliberate plugin migration requires a fresh scaffold:

1. Generate the ignored plugin input with `make build-installer` immediately
   before running the plugin.
2. Review the generated chart as a migration rather than a mechanical update.
3. Restore and validate the maintained public values and template behavior.

The vendored Microsoft webhook chart lives in
`dist/vendor/workload-identity-webhook`. Its
[README](../vendor/workload-identity-webhook/README.md) records the upstream
source, license, local changes, image digest, and update procedure.

## Maintained files

| Path | Purpose |
| --- | --- |
| `Chart.yaml` | Chart metadata and the vendored webhook dependency |
| `Chart.lock` | Locked dependency version and digest |
| `values.yaml` | Default public values contract |
| `values.schema.json` | Machine-validated public values contract |
| `values-production.yaml` | Explicit production availability profile |
| `values-single-replica.yaml` | Local and constrained-cluster profile |
| `templates/` | Maintained Kubernetes and OpenShift resources |

When public chart behavior changes, update the schema and relevant published
documentation in the same change. Keep operational explanations in the docs
instead of duplicating them here.

## Validation

Run the repository entry points from the project root:

```bash
make helm-dependency
make helm-lint
```

Run `make test-chart-integration` only against the prepared disposable cluster
described in the [test guide](../../test/README.md).
