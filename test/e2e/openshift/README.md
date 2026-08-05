# OpenShift/CRC Packaged E2E Test

This target validates the production Helm packaging on a disposable local CRC
cluster while exercising real Azure resources. It builds the operator with an
OpenShift binary `BuildConfig`, stores the image in the internal OpenShift
registry, installs the complete chart, and routes admission through the real
API server.

## Dependencies

Every run requires:

- CRC
- Go
- OpenShift CLI (`oc`)
- Helm
- Azure CLI (`az`)
- Git
- Make
- curl
- OpenSSL

Release-candidate validation additionally requires:

- an authenticated GitHub CLI (`gh`)
- `jq`
- `sha256sum`

Required local state:

- the current kubeconfig context authenticated to CRC as `kubeadmin`
- all OpenShift ClusterOperators healthy
- Azure CLI authenticated to the subscription used for test orchestration
- permission for the Azure CLI identity to create/delete Entra applications
- permission for the Azure CLI identity to create/delete role assignments at
  the test subscription scope

## Safety

Use a fresh, dedicated CRC cluster. The test changes
`Authentication/cluster.spec.serviceAccountIssuer`, which rolls the OpenShift
API server. It refuses any API server except `https://api.crc.testing:6443`,
requires `oc whoami` to be `kubeadmin`, and requires every ClusterOperator to
be healthy. Never run it against a shared, development, staging, or production
cluster.

The Azure CLI identity creates test role assignments, Key Vault resources, and
cleanup operations. By default it also creates a unique Entra application and
Service Principal for the in-cluster operator, grants that principal temporary
`Contributor` access at subscription scope, and deletes the assignment and
application during cleanup. Subscription scope is required because this test
proves that the operator can create its configured resource group. Override
`OPERATOR_AZURE_RESOURCE_ROLE` only when an equivalent custom role exists.

Tenants that prohibit application creation can supply a complete existing
Service Principal credential trio instead. In that mode, the script reuses the
identity and creates only the test-specific role assignments it needs. Set
`OPERATOR_AZURE_PRINCIPAL_ID` when directory lookup is unavailable. The
operator identity type is fixed to `ServicePrincipal` and is not configurable.

The script separately resolves the active Azure CLI identity's own Entra
object ID when it grants that identity permission to upload the Key Vault test
secret. If that lookup is unavailable, set `AZURE_CLI_PRINCIPAL_ID` together
with `AZURE_CLI_PRINCIPAL_TYPE`; the supported types are `User` and
`ServicePrincipal`. These values describe the test orchestrator, not the
in-cluster operator identity.

By default, the script requires an unused Key Vault name, creates the vault,
and deletes and purges only that script-created vault during cleanup. With
`ENSURE_KEY_VAULT=false`, `KEY_VAULT_NAME` must identify an existing vault; the
test retains the vault itself and requires `UPLOAD_KEYVAULT_SECRET=false` so it
cannot replace an externally owned secret. Supply `KEY_VAULT_SECRET_NAME` and
`KEY_VAULT_SECRET_VALUE` for the existing secret the test should read. Any
temporary role assignments created by the test are still removed. Creation
mode fails closed on live or soft-deleted name collisions and never purges a
vault it did not create.

Reset and verify CRC according to the repository `AGENTS.md` before every full
pass. In particular, keep the shell that started CRC open for the duration of
the test. The script fails closed when operator CRDs, either operator namespace,
either cluster-wide webhook configuration, or the Helm release already exists;
it never adopts or automatically removes an installation it did not create.

## Credentials

The recommended entry point needs no long-lived operator credential:

```bash
make test-e2e-crc
```

The generated client secret exists only in the script's mode-`0700` temporary
directory and the temporary Kubernetes Secret. Individual files are mode
`0600`, secret literals never appear in command-line arguments or Helm values,
and shell tracing is never enabled. Cleanup removes the Kubernetes Secret,
temporary directory, role assignments, and Entra application.

For a restricted tenant, inject an existing credential into the process
environment as a complete trio:

```bash
export AZURE_CLIENT_ID AZURE_TENANT_ID AZURE_CLIENT_SECRET
make test-e2e-crc
```

Do not put the client secret in a repository-local `.env` file or pass it as a
Make command-line variable. A secret broker such as
[AgentSecrets](https://github.com/the-17/agentsecrets) can inject the fallback
values into the process without storing them in the worktree; this repository
does not read or manage the broker's secret store. Exporting from another
trusted shell or secret manager is also supported.

To validate a release candidate rather than rebuilding the operator in
OpenShift, check out the candidate commit and set the non-secret
`OPERATOR_CANDIDATE_RUN_ID` printed by the candidate workflow. The script uses
the authenticated GitHub CLI to download that run's immutable artifact,
verifies its workflow provenance, metadata, and checksums, and installs its
exact chart archive. It requires a clean worktree at the recorded commit and
verifies that the Deployment uses the archive's exact image digest:

```bash
export OPERATOR_CANDIDATE_RUN_ID=<candidate-workflow-run-id>
make test-e2e-crc
```

Optional repository, digest, and commit environment values are accepted only as
cross-checks against the downloaded metadata.

In every mode, the script copies credentials into mode-`0600` temporary files
and creates the Kubernetes Secret with `--from-file`. The client secret is then
removed from the script environment.

## Run

From the repository root:

```bash
make test-e2e-crc
```

Use `./test/e2e/openshift/e2e-test.sh --help` for all overrides. Defaults use:

- operator namespace: `azure-workload-identity-operator-system`
- bundled webhook namespace: `microsoft-azure-workload-identity-webhook-system`
- test namespace: `azwi-crc-test`
- shared operator-owned resource group: `rg-azwi-crc-platform-test-<run-id>`
- script-owned Key Vault resource group: `rg-azwi-crc-kv-test-<run-id>`
- storage account: `stazwicrc<run-id>`
- Key Vault: `kv-azwi-<run-id>`

The run ID is eight lowercase hexadecimal characters generated by
`openssl rand -hex 4`. The same short value groups the run's Azure resources
without approaching Azure naming limits. Explicit environment overrides still
take precedence.

## What the test proves

The numbered script flow is:

1. Create the ephemeral operator identity (or validate the supplied fallback),
   verify test Azure groups are absent, and install pinned cert-manager when the
   cluster does not already own it.
2. Build an allowlisted source context through OpenShift and push the image to
   the internal registry via `BuildConfig`/`ImageStream`, or use an explicitly
   supplied release-candidate chart archive and its embedded digest. The
   build context contains only the Dockerfile, Go module files, and Go source
   trees; ignored env files and repository metadata are never uploaded.
3. Install the first-party chart with the required Azure scope and an existing
   credentials Secret. This also installs the bundled Microsoft Azure Workload
   Identity mutating webhook.
4. Verify both deployments in their separate namespaces, both cert-manager
   Certificates, both injected CA bundles, three `failurePolicy: Fail`
   validating webhooks, least-privilege mutating-webhook RBAC, and restricted
   SCC admission without any fixed UID/GID patch.
5. Create the workload test namespace.
6. Create `OIDCIssuer/default` through real API-server admission.
7. Grant the operator Service Principal blob data access to published OIDC
   documents.
8. Wait for the issuer to become Ready and publish discovery/JWKS documents.
9. Verify capture of the previous OpenShift service-account issuer.
10. Verify active and retiring signing keys are published.
11. Wait for OpenShift to roll and mint tokens with the published issuer.
12. Replace only the retiring key Secret and verify packaged-controller periodic
    refresh republishes it.
13. Create a real Azure Key Vault.
14. Create `WorkloadIdentity`, validate immutable naming and ownership tags,
    scale the packaged operator down/up to test deterministic ServiceAccount
    replacement, and verify provenance repair.
15. Verify controller ownership conflict reporting and real admission rejection
    of a duplicate Azure identity owner.
16. Upload a Key Vault secret and grant workload access.
17. Build the reader image with an OpenShift `BuildConfig` and run the Job from
    the internal registry.
18. Prove the injected workload identity reads the real Key Vault secret.
19. Drift the Azure federated credential and verify the packaged controller's
    periodic reconcile repairs it.
20. Retain and recreate `WorkloadIdentity`; exercise real recovery admission,
    immutable spec, duplicate-source rejection, blocked-without-mutation,
    same-object resume, forward-only commit checkpoints, ownership transfer,
    and Key Vault access after recovery.
21. Verify real admission rejects unsafe OIDCIssuer deletion while a
    WorkloadIdentity exists.
22. During cleanup, verify real admission also rejects deletion while OpenShift
    still references the issuer.
23. Restore the original OpenShift issuer, complete deletion/finalizers, verify
    the shared resource group was retained, and clean up test ownership.

This test intentionally does not curl local webhook handlers or run a local
operator process. Its admission assertions exercise the chart-installed
`ValidatingWebhookConfiguration` and service.

## Cleanup ownership

The exit trap deletes CRs before uninstalling the chart so Azure finalizers can
run. It then verifies Helm retained the operator CRDs, explicitly deletes those
test-owned CRDs, removes the operator credentials Secret, OpenShift build
artifacts, operator release/namespace, and test Azure groups. If this run
installed cert-manager into an otherwise clean cluster, it also removes that
release, its retained CRDs, and namespace. Pre-existing cert-manager resources
are reused and retained.

When the run created the operator identity, cleanup also deletes every tracked
role assignment and the ephemeral Entra application (which removes its home
tenant Service Principal). A cleanup failure prints the application ID for
manual removal.

The chart-retained `microsoft-azure-workload-identity-webhook-system` Namespace
is also deleted because the clean-target check proves this disposable run
created it.

The chart itself retains CRDs during a normal production uninstall; explicit
CRD deletion here is test cleanup for the disposable cluster, not production
guidance.

## Troubleshooting

- A quiet interval can be normal during Azure RBAC propagation or OpenShift API
  server rollout. Inspect the operator namespace, Helm release, and cluster
  operators before assuming a code defect.
- If the operator build fails, inspect Builds and build Pods in
  `azure-workload-identity-operator-system`.
- If pods do not start, inspect their `openshift.io/scc` annotation and events;
  no post-install UID/GID patch should be necessary.
- If OAuth or `oc` becomes unauthorized during issuer handoff, open a fresh
  shell, run `eval $(crc oc-env)`, and log in again with the password from the
  current CRC start.
- If CRC is under disk pressure, delete old builds, images, and stopped CRC
  state, then repeat from a fresh `crc delete -f`.
