# TODO

- Support safe signing key rotation by publishing multiple JWKS keys during the token lifetime overlap. Current OIDC document generation publishes one key, so replacing the signing key immediately replaces JWKS content.
- Document that users must set `OIDCIssuer.spec.openShift.updateServiceAccountIssuer` to `false` before manually restoring the OpenShift service account issuer; otherwise, the operator may reconcile the restore back to the `OIDCIssuer` URL.
