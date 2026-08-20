# azure-workload-identity-operator

A Kubernetes operator for managing Azure workload identity infrastructure and
the cluster's OpenID Connect issuer integration.

The [project documentation][documentation] explains how to install, configure,
use, and operate the operator.

## Project background

This project was inspired by [Stakater's Azure Workload Identity
Operator][stakater-operator]. It is an independent implementation, not a fork,
and is not affiliated with Stakater or Microsoft.

The design goals differed enough that a separate implementation was more
appropriate than asking the Stakater project to adopt a fundamentally different
operating model. This project therefore focuses on strict Azure resource
ownership, ownership-verified deletion, periodic drift reconciliation,
overlapping JWKS publication during externally managed signing-key rotation,
and controlled recovery of retained workload identities.

## Contributing

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for project
expectations and guidance on getting started.

[documentation]: https://onurmicoogullari.github.io/azure-workload-identity-operator/
[stakater-operator]: https://github.com/stakater/azure-workload-identity-operator
