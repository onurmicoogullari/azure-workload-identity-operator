# Kind packaged upgrade and rollback E2E

This dedicated Go suite proves the portable Kubernetes lifecycle of two real,
locally built operator packages. It does not share ordered state with the
Kustomize-oriented manager suite in `test/e2e/kind`, and it does not replace
the same-chart lifecycle tests in `test/integration/chart`.

## Manual revision selection

This is a manual release-qualification test. It is intentionally not a pull
request or push check: selecting the last supported baseline is a release
decision, and the required container runtime, Kind, packaging, and cluster work
is too heavy for routine CI.

Pass both revisions as Make variables. They may be tags, branches, abbreviated
hashes, or other unambiguous Git revisions:

```bash
make test-e2e-kind-upgrade \
  BASELINE_REF=<last-supported-release-or-commit> \
  CANDIDATE_REF=<candidate-commit>
```

Make resolves both inputs to full commits and rejects missing, unresolved, or
equal revisions before creating Kind. The resolved commits are passed to the Go
suite, which validates them again. Each commit is exported into a separate
temporary source tree. The suite builds and loads an operator image tagged with
that full commit and packages a Helm archive whose `appVersion` contains the
full commit. Its label-safe chart version contains the package role and the
commit's first 12 characters. Neither input is published or pulled from a
release registry.

For automation or scripting, the existing environment-variable form remains
supported through `UPGRADE_E2E_BASELINE_REF` and
`UPGRADE_E2E_CANDIDATE_REF`; explicit `BASELINE_REF` and `CANDIDATE_REF` Make
variables take precedence.

## What it proves

On a disposable, pinned Kind node image, the suite installs pinned cert-manager
and then:

1. installs the packaged baseline chart and exact baseline image;
2. creates portable `OIDCIssuer` and `WorkloadIdentity` objects using fake Azure
   identifiers, then captures their specifications and UIDs;
3. upgrades to the packaged candidate and verifies Helm revision progression,
   both Deployments, both serving Certificates and admission configurations,
   the exact candidate image, CR retention, CRD served/storage state, singleton
   Helm ownership, the valid admission path for representative CRs, and
   bundled Pod mutation;
4. renders both packaged charts and discovers their complete CRD inventories,
   then compares API-server-normalized state for every CRD in their union;
5. rolls Helm back to revision 1 only when every baseline CRD retained its UID
   and exact specification and every candidate-only CRD has no stored objects,
   then verifies revision 3, the exact baseline image, all retained CRDs and
   objects, ownership, the valid admission path, and mutation again;
6. otherwise reports and tests a roll-forward-only result: no Helm rollback is
   attempted, revision 2 remains deployed, and the candidate and retained
   objects remain healthy.

A CRD addition does not by itself block rollback. An empty candidate-only CRD
is retained in the cluster while the baseline controller is restored. A CRD
removed from the candidate package is also compatible when the baseline CRD
and its objects remain unchanged. A rename is evaluated as those two operations:
the old CRD must remain intact and the new CRD must be empty. Candidate-only
objects block rollback because the baseline controller cannot manage them.
Changes to a baseline CRD specification remain roll-forward-only because an
older schema can prune or reject data added by a newer schema; relaxing that
rule requires a tested storage migration and explicit downgrade contract. See
the Kubernetes documentation for
[CRD versioning](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/)
and Helm's [CRD lifecycle guidance](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/).

## What it does not prove

The suite does not authenticate to Azure, create Azure resources, wait for
Azure reconciliation to become Ready, validate OpenShift behavior, publish a
release candidate, or repeat same-chart certificate rotation, fail-closed
admission, or semantic validating-webhook rule coverage. It verifies that valid
representative CRs still pass admission after upgrade and rollback. It is not a
continuous regression check and does not select the supported baseline on the
release engineer's behalf. CRC and real-Azure validation remain separate and
require explicit approval.

## Run locally

Podman or Docker, plus Git, Go, Helm, Kind, and kubectl are required. A cold run
needs network access for the pinned Kind node and cert-manager chart/images,
the Dockerfile base images and Go modules, and the chart's pinned bundled Azure
workload identity webhook image. Already-cached inputs can be reused. The
runner validates the same Kind `v0.32.0` and Helm `v4.2.3` versions used by the
repository workflows; the expected versions can be explicitly changed with
`KIND_UPGRADE_VERSION` and `HELM_UPGRADE_VERSION`. Both revisions must already
exist in the local Git object database, and the candidate must be committed
before it can be qualified. The test itself never fetches Git refs.

```bash
make test-e2e-kind-upgrade \
  BASELINE_REF=v0.1.0 \
  CANDIDATE_REF=HEAD
```

The repository defaults to Docker. For Podman, select it through the existing
container-tool variable; the Make target also selects Kind's Podman provider:

```bash
make test-e2e-kind-upgrade \
  BASELINE_REF=v0.1.0 \
  CANDIDATE_REF=HEAD \
  CONTAINER_TOOL=podman
```

Kind's Podman provider may require additional rootless-host configuration; see
the upstream [Kind rootless provider guide](https://kind.sigs.k8s.io/docs/user/rootless/).

The Make target creates and owns
`azure-workload-identity-operator-upgrade-e2e`. It refuses to reuse a cluster
with that name, removes partial Kind creation on failure, and uses temporary
`KUBECONFIG`, `HELM_CONFIG_HOME`, `HELM_CACHE_HOME`, and `HELM_DATA_HOME`
locations so the run does not read or modify the user's Kubernetes context or
Helm repositories. Baseline and candidate images receive run-specific tags and
those host-side tags are removed after the test. Failure to remove either tag
fails an otherwise successful run.

Set `UPGRADE_E2E_KEEP_CLUSTER=true` to retain the cluster and its isolated
kubeconfig for manual inspection; the runner prints their location. On failure,
the Go suite captures Helm history/status, objects, CRDs, admission resources,
events, and workload logs before image and cluster cleanup. If cluster cleanup
itself fails, the isolated kubeconfig is preserved and the target reports the
cleanup failure.
