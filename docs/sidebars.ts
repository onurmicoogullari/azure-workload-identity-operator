import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  docs: [
    {
      type: 'category',
      label: 'Getting started',
      collapsed: false,
      items: [
        'getting-started/overview',
        'getting-started/installation',
        'getting-started/quickstart',
        'getting-started/verify',
      ],
    },
    {
      type: 'category',
      label: 'Concepts',
      items: [
        'concepts/resource-model',
        'concepts/ownership-and-lifecycle',
        'concepts/authentication',
      ],
    },
    {
      type: 'category',
      label: 'Guides',
      items: [
        'guides/oidc-issuer',
        'guides/workload-identity',
        'guides/key-rotation',
        'guides/recovery',
        'guides/gitops',
        'guides/upgrades-and-uninstall',
      ],
    },
    {
      type: 'category',
      label: 'Operations',
      items: [
        'operations/compatibility',
        'operations/permissions',
        'operations/telemetry',
        'operations/troubleshooting',
      ],
    },
    {
      type: 'category',
      label: 'Architecture',
      items: [
        'architecture/overview',
        'architecture/oidc-publishing',
        'architecture/workload-identity',
        'architecture/recovery',
        'architecture/security',
        {
          type: 'category',
          label: 'Decision records',
          items: [
            'architecture/decisions/index',
            'architecture/decisions/single-azure-scope',
            'architecture/decisions/retain-by-default',
            'architecture/decisions/controlled-recovery',
          ],
        },
      ],
    },
    {
      type: 'category',
      label: 'Reference',
      items: [
        'reference/oidcissuer',
        'reference/workloadidentity',
        'reference/workloadidentityrecovery',
        'reference/status-conditions',
        'reference/helm-values',
      ],
    },
    {
      type: 'category',
      label: 'Contributing',
      items: [
        'contributing/development',
        'contributing/testing',
        'contributing/documentation',
        'contributing/releasing',
        'contributing/vulnerability-scanning',
      ],
    },
  ],
};

export default sidebars;
