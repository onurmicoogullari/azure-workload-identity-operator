# azure-workload-identity-operator

## Support status

The operator is currently **OpenShift-first**. The Helm chart and controller
remain portable Kubernetes APIs, and chart lifecycle plus admission are tested
on Kind, but the complete Azure issuer and token-exchange path is production
verified only on OpenShift through CRC. Full vanilla Kubernetes/Azure e2e
coverage is planned in a follow-up branch; until that exists, non-OpenShift use
is compatibility preview rather than a production support claim.

## Deploy

The supported installation package is the first-party Helm chart under
`dist/chart`. It installs the operator, CRDs, validating webhooks, certificate
resources, and the bundled Azure Workload Identity mutating webhook. Install
cert-manager first, then provide the required platform-owned Azure scope and an
existing credentials Secret:

```bash
helm dependency build --skip-refresh ./dist/chart
helm install azure-workload-identity-operator ./dist/chart \
  --namespace azure-workload-identity-operator-system \
  --create-namespace \
  --set-string azure.tenantId='<tenant-id>' \
  --set-string azure.subscriptionId='<subscription-id>' \
  --set-string azure.resourceGroupName='<resource-group>' \
  --set-string azure.location='<location>' \
  --set-string azure.credentials.existingSecret='<secret-name>'
```

The Secret contains `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, and
`AZURE_CLIENT_SECRET`; secret values are never Helm values. See the
[chart documentation](dist/chart/README.md) for the complete contract,
OpenShift behavior, workload-identity migration, upgrades, and uninstall.
The operator follows the chosen Helm release namespace; the bundled mutating
webhook is isolated in the chart-owned
`microsoft-azure-workload-identity-webhook-system` namespace.

The Kustomize manifests remain Kubebuilder development inputs and are not a
second supported production packaging contract.

## E2E Tests

The local OpenShift/CRC e2e test lives in `test/e2e/openshift/`.

```bash
make test-e2e-crc
```

By default, the Azure CLI identity creates a short-lived Entra application and
Service Principal for the in-cluster operator, grants its temporary test role,
and deletes both during cleanup. A complete exported `AZURE_CLIENT_ID`,
`AZURE_TENANT_ID`, and `AZURE_CLIENT_SECRET` trio remains available as a
fallback for tenants that prohibit application creation.

See `test/README.md` for the test layout and
`test/e2e/openshift/README.md` for OpenShift-specific prerequisites, behavior,
and troubleshooting.

## Operations

- [Azure and Kubernetes permissions](docs/permissions.md)
- [Controlled workload identity recovery](docs/recovery.md)
- [Release process](docs/releasing.md)
