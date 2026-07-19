---
name: run-crc-e2e
description: Reset a disposable local Red Hat CodeReady Containers (CRC) cluster, keep its VM session alive, authenticate to OpenShift as kubeadmin, run this repository's OpenShift/Azure end-to-end test, and verify cleanup. Use when asked to run, rerun, validate, or troubleshoot `e2e/openshift/e2e-test.sh` against CRC.
---

# Run CRC E2E

Use a fresh disposable CRC cluster. The e2e script mutates OpenShift authentication, creates real Azure resources, runs the operator locally, and deletes its test resources.

## Preconditions

- Work from the repository root.
- Confirm `crc`, `oc`, `kubectl`, `az`, `helm`, `make`, and `go` are available.
- Confirm Azure CLI authentication before starting.
- Treat the kubeadmin password printed by `crc start` as ephemeral sensitive data. Do not repeat it in summaries or persist it in repository files.

## Reset CRC

Run these commands in order:

```bash
crc delete -f
crc setup
```

Do not reuse an existing CRC VM for this verification.

## Start CRC in a keeper session

Open a dedicated interactive shell session, run `crc start` inside it, and leave that shell open until the e2e script and cleanup have finished. Do not use a fixed sleep or let the shell that launched CRC exit.

Capture the fresh kubeadmin password from the successful `crc start` output. Do not reuse a password from an earlier VM.

If CRC reports damaged host-side state after its launching session was closed, run `crc setup` again and restart CRC in a new keeper session.

## Authenticate and verify readiness

In a separate shell, load CRC's `oc` environment before every `oc` command group:

```bash
eval $(crc oc-env)
oc login -u kubeadmin -p '<fresh-password>' https://api.crc.testing:6443
oc whoami
oc wait clusterversion/version --for='condition=Available=True' --timeout=10m
oc get clusteroperators
```

Require `oc whoami` to return `kubeadmin`. Require every cluster operator to report `Available=True`, `Progressing=False`, and `Degraded=False` before running the test.

## Run the e2e test

Keep the CRC keeper session open. From a separate shell at the repository root, refresh the `oc` environment and login, then run:

```bash
eval $(crc oc-env)
oc login -u kubeadmin -p '<fresh-password>' https://api.crc.testing:6443
./e2e/openshift/e2e-test.sh
```

Poll long-running output regularly. The script can be quiet while OpenShift rolls API servers or Azure RBAC propagates. Before diagnosing a timeout, inspect the cluster state and the operator log path printed by the script.

If OpenShift issuer changes cause `Unauthorized`, connection resets, or stale watches, open a fresh shell, run `eval $(crc oc-env)`, and repeat the kubeadmin login with the captured password.

## Evaluate and clean up

Require a zero exit code and the Key Vault reader success message. Confirm the script completes WorkloadIdentity, OIDCIssuer, webhook, namespace, role-assignment, and Azure cleanup.

For the default configuration, verify all test resource groups are absent:

```bash
az group exists -n rg-azwi-crc-storage-test
az group exists -n rg-azwi-crc-wi-test
az group exists -n rg-azwi-crc-kv-test
```

Require all three commands to return `false`.

Confirm the CRC cluster is healthy after the issuer handoff and cleanup:

```bash
eval $(crc oc-env)
oc get clusteroperators
```

Require every cluster operator to report `Available=True`, `Progressing=False`,
and `Degraded=False`.

Leave the CRC keeper session and cluster running unless the user asks to stop them. If the e2e test exposes a code or script defect, fix it and repeat the entire workflow from `crc delete -f`.
