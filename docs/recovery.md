# Controlled Workload Identity Recovery

`WorkloadIdentityRecovery` transfers an operator-created, retained Azure user
assigned managed identity (UAMI) from an earlier `WorkloadIdentity` instance to
the current instance with the same namespace and name. It is cluster scoped so
recovery access can be restricted independently from namespaced
`WorkloadIdentity` access.

The operator never adopts an arbitrary UAMI. Normal reconciliation first
verifies the exact derived Azure resource ID, operator-created ownership tags,
stable logical key, and previous Kubernetes UID. When those checks identify an
earlier instance, the current `WorkloadIdentity` reports:

```yaml
status:
  recovery:
    previousWorkloadIdentityUid: <previous-uid>
  conditions:
    - type: Ready
      status: "False"
      reason: RecoveryRequired
```

No Azure or ServiceAccount writes occur during normal `RecoveryRequired`
reconciliation.

## Start a recovery

Admission requires all of the following:

- `spec.workloadIdentityRef` matches the namespace, name, and current UID of an
  existing, non-deleting `WorkloadIdentity`
- the target's current observed generation is `Ready=False` with reason
  `RecoveryRequired`
- `spec.previousWorkloadIdentityUid` exactly matches
  `status.recovery.previousWorkloadIdentityUid`
- no other visible `WorkloadIdentityRecovery` exists for that previous UID

Copy the live values:

```bash
kubectl get workloadidentity -n <namespace> <name> -o yaml
```

Then create a recovery:

```yaml
apiVersion: workloadidentity.azure.micosolutions.se/v1alpha1
kind: WorkloadIdentityRecovery
metadata:
  name: recover-example
spec:
  workloadIdentityRef:
    namespace: example
    name: example
    uid: <current-workloadidentity-uid>
  previousWorkloadIdentityUid: <status.recovery.previousWorkloadIdentityUid>
```

The spec is immutable. If a recovery is `Blocked`, correct the reported
external conflict and keep the same object; reconciliation resumes
automatically.

The admission duplicate check gives an immediate error during ordinary use,
but it is intentionally advisory because a cache can briefly lag. The
controller therefore performs an authoritative, uncached duplicate check
before recovery starts. An already-started recovery remains the incumbent.
Otherwise, the oldest recovery for a previous UID proceeds. Other objects
become terminal `Failed=True` with reason `DuplicateRecovery`. Recovery names
are arbitrary and do not determine uniqueness.

## Forward-only recovery

Before mutation, the controller:

- verifies the exact UAMI path, operator ownership tags, logical key, previous
  UID, and immutable Azure identity properties
- enumerates the complete FIC set and requires zero FICs or exactly one with
  the configured name and derived ARM resource ID
- verifies that any existing ServiceAccount is missing, safely unowned,
  source-owned by this operator, or already transferred
- persists a small recovery plan containing the verified Azure identity and
  exact issuer, subject, and audience tuple

A differently named FIC, multiple FICs, foreign ServiceAccount ownership, or
ambiguous metadata sets `Blocked=True` without mutation.

Once `status.mutationStarted` is true, recovery is deliberately forward-only.
The controller performs these idempotent steps:

1. mark the target `RecoveryInProgress`
2. write and read-verify the recovery UID and target UID fencing tags on the
   UAMI
3. create or repair the configured FIC and read-verify its exact trust tuple
4. transfer a safely recoverable ServiceAccount using an optimistic-lock patch
   and read verification
5. transfer the UAMI owner UID and record the recovery UID while retaining both
   Azure fencing tags
6. persist `status.commitVerified: true`
7. read-verify the committed UAMI and FIC again, then clear the Azure fencing
   tags
8. persist `Complete=True`
9. mark the target `RecoveryCompleted`, allowing normal reconciliation to
   resume

The Azure fence spans the entire interval from the first external mutation
through the durable Kubernetes commit checkpoint. Normal reconciliation checks
both Kubernetes recovery state and the Azure fence. Consequently, a cached or
already-running normal reconcile still receives `RecoveryInProgress` from
Azure and cannot cross the pre-checkpoint ownership transition.

A missing ServiceAccount is left for normal reconciliation after completion.
If one appears immediately before commit, recovery retries its validation and
transfer. A concurrent update or same-name replacement is never overwritten
because transfer includes the ServiceAccount resource version and read-verifies
the exact instance before commit.

Immediately before ownership transfer, the controller uses uncached Kubernetes
reads to verify the recovery, target UID, recovery evidence, and ServiceAccount.
The Azure commit re-enumerates the full FIC set. A crash after Azure ownership
transfer is safe: the fence remains active, the next reconciliation recognizes
the committed-and-fenced state, and the same object continues forward.

The recovery controller runs one reconciliation worker. Production manifests
also use controller-manager leader election, so only one active recovery worker
operates even when multiple manager replicas are configured.

`Blocked` is nonterminal and retried. Before mutation, an invalid target or
duplicate source can become terminal `Failed`. After mutation starts, failures
remain nonterminal because the only safe action is to continue forward.
`Complete` is terminal and records a read-verified transfer with the Azure fence
removed.

While the target reports `RecoveryInProgress`, normal `WorkloadIdentity`
reconciliation performs no Azure, ServiceAccount, or status mutation and polls
at a bounded interval. Target deletion retains its finalizer until recovery
completes. `deletionPolicy` remains mutable during recovery and is applied after
the target is released.

If the OIDC issuer is temporarily missing or not ready during
`RecoveryRequired`, normal reconciliation preserves the recovery evidence.
Recovery preflight remains `Blocked` and resumes when the issuer becomes ready.

## Deletion and audit history

Recovery deletion admission is always allowed:

- before mutation, deletion cancels the recovery, releases an
  `RecoveryInProgress` target if necessary, and removes the recovery record
- after mutation starts, the finalizer runs the same forward recovery steps to
  completion and then removes the recovery record
- after completion, deletion removes only the audit record

A started recovery can therefore remain in `Terminating` while an external
conflict is unresolved. This is intentional: deleting the approval does not
abandon a fenced UAMI in a partially transferred state. Correct the reported
conflict and the terminating recovery continues automatically.

Recovery records have no owner reference or TTL and are not automatically
deleted. Keep completed records for audit history unless a recovery
administrator deliberately removes them.

## Downgrade safety

Do not downgrade to an operator version that does not support
`WorkloadIdentityRecovery` while a recovery is active or deleting. That version
cannot finish a forward recovery or remove its finalizer.

Before downgrading:

1. prevent recovery administrators from creating new recovery records
2. allow every started recovery to complete, including terminating recoveries
3. export completed records if their audit history must be retained, then
   delete every recovery and wait until none remain
4. verify retained UAMIs have neither the
   `workload-identity-recovery-uid` nor
   `workload-identity-recovery-target-uid` fencing tag
5. only then deploy the older operator version

If an emergency downgrade strands a recovery, redeploy the last
recovery-capable version. Do not manually remove the finalizer or edit Azure
fencing tags.

## RBAC

The Kustomize RBAC package includes the same unbound helper-role pattern as the
other APIs:

- `workloadidentityrecovery-viewer-role`
- `workloadidentityrecovery-editor-role`
- `workloadidentityrecovery-admin-role`

These roles are not bound to users by the operator. Bind a write-capable role
only to cluster administrators authorized to start or delete recoveries.
Existing `WorkloadIdentity` and `OIDCIssuer` roles receive no recovery powers.
