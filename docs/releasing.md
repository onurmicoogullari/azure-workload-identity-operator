# Releasing

Published releases contain two immutable, versioned artifacts:

- `ghcr.io/onurmicoogullari/azure-workload-identity-operator:vMAJOR.MINOR.PATCH`
- `oci://ghcr.io/onurmicoogullari/charts/azure-workload-identity-operator`

The controller image supports `linux/amd64` and `linux/arm64`. The published
chart points at the validated multi-platform image digest, not merely its tag.

## 1. Build the candidate

Run the `Release Candidate` workflow on the intended commit and supply the
semantic version without a `v` prefix. It runs Go and Helm validation, builds
one multi-platform image, pushes only a `candidate-<commit>` tag, and uploads a
14-day `release-candidate` artifact containing:

- the proposed final Helm package;
- the full candidate commit;
- the exact multi-platform image digest; and
- checksums for the metadata and chart archive.

The candidate chart is already versioned for the release and embeds that exact
digest. Record the workflow run ID printed in its summary.

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
