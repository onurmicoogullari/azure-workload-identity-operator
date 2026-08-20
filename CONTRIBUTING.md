# Contributing

Contributions are welcome. Bug reports, code improvements, tests,
documentation, and thoughtful design proposals all help improve the project.

This project values respectful, focused collaboration. Assume good intent,
explain trade-offs clearly, and direct feedback at the change rather than the
person proposing it.

## Before you start

- Search existing issues and pull requests before starting work.
- Small, focused fixes may be submitted directly as pull requests.
- Open an issue before implementing a substantial API, security, ownership,
  lifecycle, architecture, or distribution change. Early discussion helps
  avoid work that conflicts with an existing decision or project direction.
- Keep each contribution focused on one coherent problem. Separate unrelated
  cleanup or refactoring into another pull request.

## Find the relevant guide

Detailed procedures live with the documentation or the repository area they
describe:

| Area | Guide |
| --- | --- |
| Operator development and conventions | [Development guide][development] |
| Test selection and execution | [Testing guide][testing] and [test layout](test/README.md) |
| Documentation content | [Documentation guide][documentation-guide] and [site workflow](docs/README.md) |
| Helm chart source | [Chart maintenance](dist/chart/README.md) |
| Architecture and durable decisions | [Architecture overview][architecture] and [decision records][decisions] |

## Pull request expectations

A pull request should:

- explain the problem and why the proposed change is appropriate;
- describe user-facing, operational, security, and compatibility effects;
- include tests appropriate to the changed behavior and report what was run;
- update published documentation when behavior, configuration, permissions,
  conditions, or operating procedures change;
- regenerate derived files through the supported project targets rather than
  editing generated output manually;
- contain no credentials, private environment details, or unrelated changes;
  and
- pass the required continuous-integration checks.

Maintainers review contributions for correctness, safety, maintainability,
operability, test coverage, documentation, and fit with the project's scope.
Review may uncover a better approach; contributors and maintainers should work
together to reach the clearest and safest result.

Thank you for taking the time to contribute.

[architecture]: https://onurmicoogullari.github.io/azure-workload-identity-operator/architecture/overview/
[decisions]: https://onurmicoogullari.github.io/azure-workload-identity-operator/architecture/decisions/
[development]: https://onurmicoogullari.github.io/azure-workload-identity-operator/contributing/development/
[documentation-guide]: https://onurmicoogullari.github.io/azure-workload-identity-operator/contributing/documentation/
[testing]: https://onurmicoogullari.github.io/azure-workload-identity-operator/contributing/testing/
