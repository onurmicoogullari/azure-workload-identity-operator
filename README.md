# azure-workload-identity-operator

## Deploy

The default Kustomize deployment enables the OIDCIssuer validating webhook.
Install cert-manager in the target cluster before deploying this operator so
the webhook serving certificate can be issued and the
`ValidatingWebhookConfiguration` CA bundle can be injected.

Configure the required platform-owned Azure scope by appending literal
`--azure-subscription-id`, `--azure-resource-group-name`, and
`--azure-location` arguments in an installation-specific Kustomize overlay.
The committed manager base intentionally omits installation-specific Azure
values; `config/e2e` demonstrates the overlay structure with non-production
test values. These process-level settings are shared by every `OIDCIssuer` and
`WorkloadIdentity` and are intentionally not configurable in individual custom
resources. The manager rejects missing or invalid values before controllers
start.

```bash
make deploy IMG=<registry>/<image>:<tag> DEPLOY_CONFIG=config/<installation-overlay>
```

## E2E Tests

The local OpenShift/CRC e2e test lives in `e2e/openshift/`.

```bash
./e2e/openshift/e2e-test.sh
```

See `e2e/README.md` for the e2e layout and
`e2e/openshift/README.md` for OpenShift-specific prerequisites, behavior, and
troubleshooting.

## Operations

- [Azure and Kubernetes permissions](docs/permissions.md)
- [Controlled workload identity recovery](docs/recovery.md)
