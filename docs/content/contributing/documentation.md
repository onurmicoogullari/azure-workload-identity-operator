---
title: Documentation
description: Write, review, build, and maintain the Docusaurus documentation site.
---

# Documentation

Documentation is maintained as code under `docs/`. The site is intentionally not versioned yet and describes the current supported operator contract.

## Audience and depth

Every task page serves one of two readers:

- application developers who need a short path from `WorkloadIdentity` to a working Pod; and
- platform engineers who own installation, security, Azure scope, issuer lifecycle, recovery, and incident response.

Lead with the supported path and verification. Link to architecture or reference depth rather than forcing every reader through it.

## Site layout

```text
docs/
├── content/                 Markdown and MDX documentation
├── src/components/          Purpose-built site components
├── src/css/                 Accessible light and dark theme
├── static/                  Logos and static assets
├── docusaurus.config.ts     Site and navigation configuration
├── sidebars.ts              Explicit information architecture
└── package.json             Pinned Docusaurus toolchain
```

## Write a page

Use front matter with at least a title and description:

```markdown
---
title: Rotate a signing key
description: Publish old and new public keys while valid tokens overlap.
---
```

Use relative links between documents. Put task prerequisites first, then steps, verification, failure handling, and cleanup. Use Mermaid when sequence, ownership, or state is materially clearer as a diagram.

Do not copy generated CRDs or full rendered Helm manifests into prose. Summarize their public contract and link to maintained samples or exact source when necessary.

## Build locally

Node.js 20 or later is required; CI uses Node 24.

```bash
cd docs
npm ci
npm run start
```

Before review:

```bash
npm run typecheck
npm run build
```

The build fails on broken internal links. Inspect both light and dark themes and test navigation at desktop and narrow viewport widths.

## Review ownership

| Change | Required review |
| --- | --- |
| Install, upgrade, or chart values | Chart maintainer and a platform operator |
| Azure permissions or ownership | Azure implementation owner and security reviewer |
| CRD reference or conditions | API/controller owner |
| OpenShift behavior | OpenShift acceptance owner |
| Recovery protocol | Recovery implementation owner and security reviewer |
| Visual or navigation changes | Documentation maintainer; verify accessibility in both themes |

## Publication policy

Pull requests must type-check and build the complete site. Successful pushes to `main` upload the production build and deploy it through the `github-pages` environment. Repository administrators must configure **Settings → Pages → Build and deployment → Source** to use **GitHub Actions**.

Publication requires:

- no draft or placeholder pages;
- complete coverage of all three CRDs;
- an install-to-token-exchange reader path;
- architecture, security, lifecycle, troubleshooting, and recovery documentation;
- successful type check and production build; and
- an assembled-site visual review.

Code changes that alter public behavior, configuration, conditions, permissions, or operating procedures must update the corresponding documentation in the same pull request.
