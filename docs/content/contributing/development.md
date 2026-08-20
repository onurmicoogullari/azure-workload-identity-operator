---
title: Development
description: Set up the Go operator project, understand its layout, and run the manager locally.
---

# Development

The project is a Kubebuilder controller-runtime operator written in Go. Generated API and manifest files are kept in the repository and must be regenerated through the supported Make targets.

## Repository layout

```text
api/v1alpha1/             CRD Go types and generated deepcopy code
cmd/                      manager entry point and process wiring
internal/controller/      reconcilers, watches, finalizers, and status
internal/azure/           Azure SDK clients and ownership protocols
internal/oidc/            discovery and JWKS generation
internal/oidcissuer/      issuer deletion guards
internal/workloadidentity shared naming and recovery contracts
internal/webhook/         validating admission
config/                   Kustomize manifests and samples
dist/chart/               maintained public Helm chart
test/                     integration and end-to-end tests
docs/                     Docusaurus site and documentation content
```

## Prerequisites

- the Go version declared in `go.mod`;
- Make;
- Docker or a compatible container runtime for image and Kind workflows;
- Kubebuilder tooling installed through project Make targets where applicable;
- Helm for chart validation; and
- Node.js 20 or later for documentation, with Node 24 used by CI.

## Run locally

Use a disposable development cluster and confirm the current context before running a controller against it:

```bash
kubectl config current-context
make run
```

`make run` uses the current kubeconfig. It is not appropriate against a production cluster.

The process requires Azure subscription, resource group, and location flags. The chart normally supplies them; a local run must provide equivalent command-line configuration and a usable `DefaultAzureCredential` path.

## Generated files

Never edit these manually:

- `config/crd/bases/*.yaml`;
- `config/rbac/role.yaml`;
- `config/webhook/manifests.yaml`;
- `**/zz_generated.*.go`; and
- `PROJECT`.

After API types or markers change:

```bash
make manifests
make generate
```

After Go code changes:

```bash
make lint-fix
make test
```

Do not remove Kubebuilder scaffold markers. Use Kubebuilder CLI commands when introducing new APIs or webhooks.

## Controller expectations

- Reconciliation must be idempotent.
- External mutations require explicit ownership validation.
- Status uses standard Kubernetes conditions.
- Finalizers protect external cleanup and forward-only protocols.
- Secondary resources should be watched rather than polled alone.
- Logs start with a capital letter, use active or clear past tense, name the object kind, and do not end with a period.

Read the [architecture overview](../architecture/overview.md) before changing controller boundaries or ownership behavior. Add or supersede an ADR when a durable safety decision changes.
