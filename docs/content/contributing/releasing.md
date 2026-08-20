---
title: Releasing
description: Build, validate, and promote immutable operator image and Helm chart release candidates.
---

# Releasing

Published releases contain two immutable versioned artifacts:

```text
ghcr.io/onurmicoogullari/azure-workload-identity-operator:vMAJOR.MINOR.PATCH
oci://ghcr.io/onurmicoogullari/charts/azure-workload-identity-operator
```

The chart embeds the exact validated multi-platform image digest.

## 1. Build a candidate

Run the **Release Candidate** workflow from the intended commit on `main` with a semantic version without the `v` prefix.

The workflow:

1. validates the branch and version;
2. runs Go tests, source vulnerability checks, and Helm validation;
3. builds one `linux/amd64` and `linux/arm64` image index;
4. pushes only `candidate-<commit>`;
5. scans both platforms through the exact digest; and
6. packages a 14-day candidate bundle containing the proposed chart, commit, image digest, and checksums.

Record the candidate workflow run ID.

## 2. Validate the exact candidate

Check out its recorded commit in a clean worktree and run the disposable CRC/Azure path:

```bash
export OPERATOR_CANDIDATE_RUN_ID='<candidate-workflow-run-id>'
make test-e2e-crc
```

The target refuses a dirty or different worktree, downloads and verifies the candidate bundle, installs the exact chart archive and image digest, and verifies Azure cleanup.

CRC is regression coverage but does not satisfy the OpenShift 4.22.8 production gate. On the target environment, record:

- commit, chart checksum, and both platform image digests;
- OpenShift, cert-manager, optional OpenTelemetry, and Argo CD versions;
- selected security context constraint;
- first install, same-scope upgrade, Pod restart, and admission health;
- changed-scope rejection with old replicas remaining Ready;
- external Secret rotation without credentials in Git;
- normal Azure reconciliation and cleanup; and
- tracing-disabled, enabled, and exporter-failure behavior.

Any source change or new candidate run requires a fresh acceptance pass.

## 3. Promote without rebuilding

Run **Promote Release** with the successful candidate run ID. Promotion derives identity only from the verified bundle and:

1. creates the immutable Git tag at the candidate commit;
2. assigns the final image tag to the already-tested digest;
3. pushes the already-built digest-pinned chart; and
4. creates the GitHub release with the same chart archive.

Promotion is coordinated and idempotent. It verifies pre-existing artifacts are digest- or byte-identical before resuming and never rebuilds under the final version.

Registries do not provide one transaction across both OCI repositories, so a failed promotion can temporarily complete only part of the sequence. Rerun the same candidate; do not create a replacement with the same version.
