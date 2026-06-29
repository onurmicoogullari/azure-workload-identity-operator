# E2E Tests

This directory contains end-to-end smoke tests for the `OIDCIssuer` and
`WorkloadIdentity` controllers.

Each subdirectory owns one cluster-specific e2e target. The current target is
`e2e/openshift`; a future vanilla Kubernetes target can live beside it, for
example under `e2e/vanilla`.

## General Prerequisites

All e2e targets create real Azure resources and exercise a real Kubernetes API.
Before running any e2e test, make sure you have:

- A disposable or dedicated Kubernetes cluster for the target you are testing.
- An authenticated Azure CLI session.
- An Azure subscription where test resource groups and role assignments can be
  created.
- Local `kubectl`, `az`, `go`, and `make`.
- Cluster credentials with enough permission to install this operator's CRDs and
  create the target test resources.

Target-specific prerequisites are documented in each target README.
