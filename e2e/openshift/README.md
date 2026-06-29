# OpenShift E2E Test

This folder contains the OpenShift/CRC e2e target for the `OIDCIssuer` and
`WorkloadIdentity` controllers.

The test installs the pieces needed for a local OpenShift run, starts the
operator locally, creates real Azure resources, proves Azure Workload Identity
token exchange by reading a Key Vault secret from an OpenShift Job, and then
deletes the test resources again.

## Why This Target Is OpenShift-Specific

The files live under `e2e/openshift` because both the script and the manifests
depend on OpenShift behavior:

- `oidc-issuer.yaml` references the OpenShift service-account signing key Secret
  and enables the OpenShift service-account issuer update.
- `job.yaml` uses the internal OpenShift image registry.
- `e2e-test.sh` uses `oc`, OpenShift binary builds, OpenShift
  `Authentication/cluster`, and an OpenShift-specific Azure Workload Identity
  webhook patch.

## Prerequisites

Use a dedicated local CRC cluster. The script mutates OpenShift cluster
configuration by setting `Authentication/cluster.spec.serviceAccountIssuer`,
which rolls the OpenShift API server. Do not run it against a shared, dev,
stage, or production cluster.

General e2e prerequisites are listed in `../README.md`.

Additional local tools:

- `oc`
- `helm`
- `curl`

Required local state:

- CRC is installed and running.
- Your kubeconfig current context points at CRC.
- Your `oc` session has enough OpenShift privileges to install CRDs, patch
  `Authentication/cluster`, create namespaces, create OpenShift binary builds,
  install the Azure Workload Identity webhook, and read the service-account
  signing key Secret referenced by the test manifest.
- Azure CLI is authenticated and set to an account that can create the test
  resource groups and resources.
- The active Azure CLI principal can create role assignments, or you have set
  the relevant override variables described by
  `./e2e/openshift/e2e-test.sh --help`.

You do not need to install this operator, its CRDs, the Azure Workload Identity
webhook, or the Key Vault manually. The script does those things for this local
test path.

## Run

From the repository root:

```bash
./e2e/openshift/e2e-test.sh
```

The script has defaults chosen to avoid common local name collisions:

- Test namespace and service account: `azwi-crc-test`
- OIDC issuer resource group: `rg-azwi-crc-test`
- WorkloadIdentity resource group: `rg-azwi-crc-wi-test`
- Storage account: `stazwicrctest`
- Key Vault: `kv-azwi-crc-test`

Run this for the full list of overrides:

```bash
./e2e/openshift/e2e-test.sh --help
```

By default, the script expects the two Azure resource groups to be absent at the
start of the run. That is intentional: the test verifies that the operator can
delete resource groups it created.

## What The Test Proves

The test covers the full local integration path:

1. The `OIDCIssuer` controller creates Azure issuer storage and publishes OIDC
   discovery/JWKS documents.
2. The operator updates OpenShift to use the published OIDC issuer as the
   service-account token issuer.
3. OpenShift rolls the API server and starts minting service-account tokens with
   the new issuer.
4. The `WorkloadIdentity` controller creates a user-assigned managed identity,
   creates a federated identity credential, and creates or adopts the Kubernetes
   ServiceAccount.
5. The Azure Workload Identity mutating webhook injects the projected token and
   Azure environment into the test Job.
6. The test Job exchanges the OpenShift service-account token for Azure
   credentials and reads a real Key Vault secret.
7. Deleting the `WorkloadIdentity` and `OIDCIssuer` removes the Azure resources
   that the operator created.

## What The Script Does

At a high level, `e2e-test.sh`:

1. Checks required tools and resolves Azure subscription/tenant defaults from
   the active Azure CLI account.
2. Fails early if the default test resource groups already exist, so cleanup can
   be verified safely.
3. Installs the Azure Workload Identity mutating webhook with Helm.
4. Applies an OpenShift compatibility patch to the webhook Deployment. The
   upstream chart pins `runAsUser` and `runAsGroup` to `65532`; OpenShift
   assigns namespace-scoped UID ranges via SCCs, so the script removes only
   those fixed IDs.
5. Installs this operator's CRDs with `make install`.
6. Starts the operator locally with `make run`, using the current kubeconfig and
   Azure CLI-backed `DefaultAzureCredential`.
7. Creates the test namespace if it does not already exist.
8. Applies `oidc-issuer.yaml`.
9. Grants the operator identity `Storage Blob Data Contributor` on the generated
   storage account so it can upload the OIDC documents.
10. Waits for `OIDCIssuer/default` to become `Ready`.
11. Waits for OpenShift `Authentication/cluster.spec.serviceAccountIssuer` to
    match the issuer URL, waits for the kube-apiserver operator to settle, and
    verifies newly minted service-account tokens contain the expected `iss`
    claim.
12. Creates the Key Vault inside the OIDCIssuer-owned resource group. This
    intentionally makes OIDCIssuer resource-group deletion prove that the whole
    test resource group is removed.
13. Applies `workload-identity.yaml` and waits for it to become `Ready`.
14. Uploads the test secret to Key Vault and grants the workload identity
    `Key Vault Secrets User`.
15. Builds the small `keyvault-secret-reader` app with an OpenShift binary
    build, using the internal OpenShift image registry.
16. Runs `job.yaml` and waits for it to complete.
17. Prints the Job logs. A successful run includes the retrieved Key Vault
    secret value.

## Cleanup

The script registers a cleanup trap. On exit it deletes resources it created and
waits for the operator finalizers to finish:

- Test Job
- `WorkloadIdentity`
- `OIDCIssuer`
- WorkloadIdentity Azure resource group
- OIDCIssuer Azure resource group
- OpenShift BuildConfig/ImageStream created for the reader app
- Locally started operator process
- Azure Workload Identity webhook Helm release, if the script created it
- Test namespaces, if the script created them

Role assignments are not cleaned up separately. They disappear with the Azure
resources that the test deletes.

The script does not restore the previous OpenShift service-account issuer. Use a
dedicated CRC cluster and rerun the test from a clean state when needed.

## Files

- `e2e-test.sh`: Orchestrates the full local OpenShift e2e flow.
- `oidc-issuer.yaml`: Template for the cluster-scoped `OIDCIssuer/default`.
  This is OpenShift-specific because it references the OpenShift
  service-account signing key and enables the OpenShift issuer update.
- `workload-identity.yaml`: Template for the namespaced `WorkloadIdentity`.
- `job.yaml`: OpenShift test Job that reads the Key Vault secret. It uses the
  internal OpenShift image registry.
- `keyvault-secret-reader/`: Small Go app built by OpenShift and run by the Job.

## Troubleshooting

- If the script waits on service-account token issuer changes, that usually
  means OpenShift is still reconciling the API server after the issuer update.
- If secret upload or secret read fails with authorization errors, wait for
  Azure RBAC propagation or rerun the script after the assignments settle.
- If the OpenShift binary build fails because CRC is under disk pressure, remove
  old builds, pods, and images from CRC and rerun the script.
