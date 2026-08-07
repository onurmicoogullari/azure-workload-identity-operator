# Tests

The repository organizes tests by scope:

- Unit and envtest tests stay beside the Go packages they exercise as
  `*_test.go` files.
- `test/integration/<area>` contains repository-level integration tests that
  exercise multiple components against an existing test dependency or cluster.
- `test/e2e/<platform>` contains full platform workflows, including cluster and
  external-cloud setup, validation, and cleanup.
- `test/utils` contains shared test-only Go helpers.

Current cross-package suites are:

- `test/integration/chart`: Helm lifecycle, certificate, RBAC, and API-server
  admission integration tests, run against the Kind cluster created by CI.
- `test/e2e/kind`: the Kubebuilder manager deployment and metrics E2E suite on
  an isolated Kind cluster.
- `test/e2e/openshift`: the packaged OpenShift/CRC and real Azure E2E workflow.

Run them through their repository entry points:

```bash
make vulncheck              # Reachable Go vulnerability scan
make test-chart-integration # Existing prepared Kubernetes cluster
make test-e2e-kind          # Creates and removes its Kind cluster
make test-e2e-crc           # Requires a fresh, running CRC cluster
```

`config/e2e` remains under `config` because it is a Kustomize deployment fixture
consumed by the Kind E2E suite, not executable test code.

A planned follow-up branch will add a full vanilla Kubernetes/Azure target
under `test/e2e/vanilla` before non-OpenShift environments are promoted beyond
compatibility preview.

## General prerequisites

Cross-package tests use disposable or dedicated environments. Before running a
cluster or cloud test, make sure you have:

- the target-specific cluster and CLI described by that suite;
- an authenticated Azure CLI session when the suite creates real Azure resources;
- permission to install CRDs and cluster-scoped resources in the test cluster;
- local Go, Helm, and Make tooling required by the suite.

See each platform README and the Make target descriptions for exact setup and
cleanup ownership.
