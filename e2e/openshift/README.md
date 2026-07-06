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
- OIDC issuer storage resource group: `rg-azwi-crc-storage-test`
- WorkloadIdentity resource group: `rg-azwi-crc-wi-test`
- Key Vault resource group: `rg-azwi-crc-kv-test`
- Storage account: `stazwicrctest`
- Key Vault: `kv-azwi-<HHMMSS>-<random>` unless `KEY_VAULT_NAME` is set

Run this for the full list of overrides:

```bash
./e2e/openshift/e2e-test.sh --help
```

By default, the script expects the Azure resource groups to be absent at the
start of the run. That is intentional: the test verifies that the operator can
delete the OIDCIssuer and WorkloadIdentity groups it created, while the script
creates and deletes the Key Vault group it owns.

## What The Test Proves

The test covers the full local integration path:

1. The `OIDCIssuer` controller creates Azure issuer storage and publishes OIDC
   discovery/JWKS documents.
2. Signing key rotation publishes both the active OpenShift signing key and a
   test-owned retiring signing key in `OIDCIssuer/default.status.signingKeys`
   and JWKS.
3. The OIDCIssuer periodic refresh notices a replacement of the test-owned
   retiring signing key Secret without an `OIDCIssuer` spec change, then
   republishes status and JWKS with the new retiring key.
4. The operator updates OpenShift to use the published OIDC issuer as the
   service-account token issuer and records the previous configured issuer in
   `OIDCIssuer/default.status.previousServiceAccountIssuer`.
5. OpenShift rolls the API server and starts minting service-account tokens with
   the new issuer.
6. The `WorkloadIdentity` controller creates a user-assigned managed identity,
   creates a federated identity credential, and creates or adopts the Kubernetes
   ServiceAccount.
7. The Azure Workload Identity mutating webhook injects the projected token and
   Azure environment into the test Job.
8. The test Job exchanges the OpenShift service-account token for Azure
   credentials and reads a real Key Vault secret.
9. The WorkloadIdentity periodic reconcile notices a mutated Azure federated
   credential without a `WorkloadIdentity` spec change, then restores the
   expected issuer, subject, and audience.
10. The OIDCIssuer validating webhook rejects deletion while the
   `WorkloadIdentity` still exists, before the resource enters deletion.
11. After the `WorkloadIdentity` is deleted, the OIDCIssuer validating webhook
   rejects deletion while OpenShift still references the issuer URL.
12. The script performs the manual OpenShift service-account issuer handoff, then
   deletes the `OIDCIssuer` and verifies the operator removes the
   OIDCIssuer-created Azure resources.

## What The Script Tests, Step By Step

Script output is grouped under these numbered steps.

1. Install Azure Workload Identity webhook.
2. Patch webhook deployment for OpenShift UID/SCC compatibility.
3. Install operator CRDs.
4. Start the operator locally.
5. Create the test namespace.
6. Create `OIDCIssuer/default`.
7. Grant blob upload access for OIDC document publishing.
8. Wait for `OIDCIssuer/default` Ready and published issuer URL.
9. Verify previous OpenShift service-account issuer was captured.
10. Add a test retiring signing key and verify JWKS has active + retiring keys.
11. Wait for OpenShift to use the new issuer and mint tokens with that `iss`.
12. Replace the retiring key Secret only, then verify periodic refresh republishes it.
13. Create Key Vault.
14. Create `WorkloadIdentity`.
15. Verify conflict handling for ServiceAccount and federated credential conflicts.
16. Upload a real Key Vault secret and assign read access.
17. Build and run the OpenShift Job.
18. Verify the Job reads the Key Vault secret using workload identity.
19. Mutate Azure federated credential and verify WorkloadIdentity periodic reconcile repairs it.
20. Verify unsafe OIDCIssuer deletion is rejected while WorkloadIdentity exists.
21. During cleanup, verify deletion is also rejected while OpenShift still references the issuer.
22. Restore OpenShift issuer, delete resources, and verify Azure cleanup.

## Cleanup

The script registers a cleanup trap. On exit it deletes resources it created and
waits for the operator finalizers to finish:

- Test Job
- `WorkloadIdentity`
- `OIDCIssuer`
- Key Vault Azure resource group
- WorkloadIdentity Azure resource group
- OIDCIssuer Azure resource group
- Role assignments created by the script
- OpenShift BuildConfig/ImageStream created for the reader app
- Locally started operator process
- Azure Workload Identity webhook Helm release, if the script created it
- Test namespaces, if the script created them

Any remaining role assignments disappear with the Azure resources that the test
deletes.

When `OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER=true`, the script also validates
the manual handoff guard. It first confirms `OIDCIssuer/default` cannot be
deleted while `Authentication/cluster.spec.serviceAccountIssuer` still points at
the published issuer, then disables OIDCIssuer issuer management, restores the
captured OpenShift issuer value, waits for rollout, and finally deletes the
`OIDCIssuer`.

The OpenShift script runs the operator locally by default, so it exercises the
local validating webhook endpoint directly for the deletion rejection check.
The Kind e2e suite covers the deployed `ValidatingWebhookConfiguration` path.

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
