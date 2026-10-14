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
npm run audit
npm run lint
npm run typecheck
npm run build
```

## Dependency security

CI audits production and development dependencies after `npm ci` with
`npm run audit`, which fails on high or critical vulnerabilities. Run it after
updating dependencies.

`scripts/audit.mjs` allowlists advisories that have no patched release. Each
entry records why the exposure is acceptable, an owner, and an expiry date. The
audit fails after the expiry until the owner reviews the entry; see
`content/contributing/vulnerability-scanning.md` for the review steps. It currently allowlists
`braces` (GHSA-vfj7-8cjw-p6xm), which is only reached at build time on glob
patterns from this repository.

The overrides in `package.json` keep transitive dependencies on patched versions
while their parents require older releases:

- `markdownlint-cli2` pins vulnerable `smol-toml`; use 1.9.0 or later.
- The webpack copy and CSS minimizer plugins require `serialize-javascript` 6;
  use 7.1.2 or later to address code execution, denial of service, and XSS
  advisories.
- `sockjs` requires `uuid` 8; use the patched CommonJS-compatible 11.1.1 release.
- The cssnano 6 plugins used by Docusaurus require `postcss-selector-parser` 6;
  use 7.1.6 or later to address quadratic selector parsing.
- `@docusaurus/core` requires `tinypool` 1; use 2.1.2 or later to address
  prototype pollution leading to code execution. Docusaurus only loads it when
  `future.faster.ssgWorkerThreads` is enabled, which this site does not do.
- `mermaid` and `micromark-extension-math` require `katex` 0.16; use 0.18.2 or
  later to address a trust-restriction bypass.

Remove each override once its parent dependencies require patched versions.
Validate changes with a clean `npm ci`, the checks above, and a local development
server smoke test, since the SockJS override affects development tooling.

Documentation content lives in `content/`; navigation lives in `sidebars.ts`.
See `content/contributing/documentation.md` for writing and review guidance.
