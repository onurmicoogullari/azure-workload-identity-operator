# GitOps samples

These samples show two ways to consume the released OCI Helm chart:

- `application.yaml` uses a native Argo CD Helm source;
- `kustomize/` supports an Argo CD Application or ApplicationSet that points at
  a Git directory.

They do not prescribe how Applications are composed. The Application sample
uses `CreateNamespace=true`; an ApplicationSet that generates an Application
for `kustomize/` should set the same sync option. Arrange for an external Secret
mechanism to create the referenced Azure credential Secret, then replace every
example value and pin the intended chart version.

See the [GitOps installation guide](../../../docs/content/guides/gitops.md) for the supported
contract and safety requirements.
