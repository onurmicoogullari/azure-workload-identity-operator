# Releasing

Published releases contain two immutable, versioned artifacts:

- `ghcr.io/onurmicoogullari/azure-workload-identity-operator:vMAJOR.MINOR.PATCH`
- `oci://ghcr.io/onurmicoogullari/charts/azure-workload-identity-operator`

The controller image supports `linux/amd64` and `linux/arm64`. The published
chart points at the validated multi-platform image digest, not merely its tag.

## 1. Build the candidate

Run the `Release Candidate` workflow from the intended commit on `main` and
supply the semantic version without a `v` prefix. The workflow rejects other
branches before building or pushing anything. It runs Go, vulnerability, and
Helm validation, builds one multi-platform image, and pushes only a
`candidate-<commit>` tag. Before packaging, Trivy scans both platforms through
the exact image-index digest and blocks fixable HIGH or CRITICAL findings. See
[Vulnerability scanning](vulnerability-scanning.md) for the scanner pinning,
unfixed-finding policy, and local source check. Only after those gates pass does
the workflow upload a 14-day `release-candidate` artifact containing:

- the proposed final Helm package;
- the full candidate commit;
- the exact multi-platform image digest; and
- checksums for the metadata and chart archive.

The candidate chart is already versioned for the release and embeds that exact
scanned digest. Record the workflow run ID printed in its summary.

## 2. Validate the exact candidate on CRC/Azure

Check out the recorded commit in a clean worktree. Reset CRC from scratch and
verify all ClusterOperators are healthy as described in `AGENTS.md`. Then run:

```bash
export OPERATOR_CANDIDATE_RUN_ID=<candidate-workflow-run-id>
make test-e2e-crc
```

By default the target creates and later deletes an ephemeral operator Service
Principal; a complete credential trio can be exported as a fallback in
restricted tenants. It refuses a dirty or different worktree, downloads and
verifies the candidate artifact, installs its exact chart archive and image
digest, exercises the packaged Helm release through real OpenShift admission
and recovery, and verifies cleanup. Confirm both test Azure resource groups are
absent afterward.

Any source change or new candidate workflow run creates a different candidate
and requires a fresh CRC/Azure pass.

## 3. Promote the validated candidate

Run the `Promote Release` workflow with the candidate workflow run ID from the
successful CRC pass. Promotion derives the commit, image digest, version, and
chart filename from that run's verified candidate bundle.

Promotion verifies the downloaded candidate metadata and checksums, confirms
the checked-out commit, chart filename and embedded image digest, and then:

1. atomically claims the version with an immutable Git tag at the candidate
   commit;
2. gives the already-tested image digest its final `vMAJOR.MINOR.PATCH` tag;
3. pushes the already-built digest-pinned chart archive; and
4. creates the GitHub release with the same chart archive.

The steps are coordinated and idempotent so a failed promotion can be safely
rerun with the same inputs. Registries do not provide a transaction spanning
both OCI repositories, so the workflow verifies any pre-existing artifact is
byte-for-byte or digest-identical before resuming; it never rebuilds during
promotion or replaces a version with different content.
