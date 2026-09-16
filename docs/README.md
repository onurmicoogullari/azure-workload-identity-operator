# Documentation site

This directory contains the Docusaurus source for the Azure Workload Identity
Operator manual. Pull requests audit dependencies, lint, type-check, and build
the site; pushes to `main` publish successful builds to GitHub Pages at
<https://onurmicoogullari.github.io/azure-workload-identity-operator/>.

Repository administrators must select **GitHub Actions** as the publishing
source under **Settings → Pages → Build and deployment**.

## Local development

Node.js 20 or later is required. CI uses Node 24.

```bash
npm install
npm run start
```

Before review:

```bash
npm audit --include=dev --audit-level=high
npm run lint
npm run typecheck
npm run build
```

## Dependency security

CI audits production and development dependencies after `npm ci` and fails on
high or critical vulnerabilities. Run the audit above after updating dependencies.
The overrides in `package.json`
keep transitive dependencies on patched versions while their parents require
older releases:

- `markdownlint-cli2` pins vulnerable `smol-toml` 1.7.0; use 1.7.1 or later.
- The webpack copy and CSS minimizer plugins require `serialize-javascript` 6;
  use 7.0.5 or later to address code execution and denial of service advisories.
- `sockjs` requires `uuid` 8; use the patched CommonJS-compatible 11.1.1 release.

Remove each override once its parent dependencies require patched versions.
Validate changes with a clean `npm ci`, the checks above, and a local development
server smoke test, since the SockJS override affects development tooling.

Documentation content lives in `content/`; navigation lives in `sidebars.ts`.
See `content/contributing/documentation.md` for writing and review guidance.
