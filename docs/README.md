# Documentation site

This directory contains the Docusaurus source for the Azure Workload Identity
Operator manual. Pull requests lint, type-check, and build the site; pushes to
`main` publish successful builds to GitHub Pages at
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
npm run lint
npm run typecheck
npm run build
```

Documentation content lives in `content/`; navigation lives in `sidebars.ts`.
See `content/contributing/documentation.md` for writing and review guidance.
