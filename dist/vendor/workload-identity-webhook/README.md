# Vendored Azure Workload Identity webhook chart

This directory is based on Microsoft Azure Workload Identity webhook chart
`1.6.0` from source ref `v1.6.0`.

The parent operator chart owns the webhook tenant ConfigMap so users configure
one `azure.tenantId` value. It also places the dependency in the fixed
`microsoft-azure-workload-identity-webhook-system` namespace and owns its cert-manager
Issuer and Certificate. The upstream certificate rotator is disabled, so the
webhook no longer needs Secret access or permission to update its
MutatingWebhookConfiguration; cert-manager injects the CA bundle instead. The
Deployment security contexts are value-driven and omit fixed UID/GID values so
OpenShift `restricted-v2` can assign a namespace-specific identity. The
upstream image is pinned by multi-platform digest.

To update the dependency, download the new upstream chart, compare every file,
reapply only these documented deltas, update the image digest and metadata, and
run Helm Kind plus fresh CRC/Azure validation.
