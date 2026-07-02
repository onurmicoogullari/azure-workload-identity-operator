# az-workload-identity-operator

## Deploy

The default Kustomize deployment enables the OIDCIssuer validating webhook.
Install cert-manager in the target cluster before deploying this operator so
the webhook serving certificate can be issued and the
`ValidatingWebhookConfiguration` CA bundle can be injected.

```bash
make deploy IMG=<registry>/<image>:<tag>
```

## E2E Tests

The local OpenShift/CRC e2e test lives in `e2e/openshift/`.

```bash
./e2e/openshift/e2e-test.sh
```

See `e2e/README.md` for the e2e layout and
`e2e/openshift/README.md` for OpenShift-specific prerequisites, behavior, and
troubleshooting.
