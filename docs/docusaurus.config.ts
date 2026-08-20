import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import {themes as prismThemes} from 'prism-react-renderer';

const repositoryUrl =
  'https://github.com/onurmicoogullari/azure-workload-identity-operator';

const config: Config = {
  title: 'Azure Workload Identity Operator',
  tagline: 'Reconciled workload identity for Kubernetes and Azure',
  favicon: 'img/favicon.svg',

  url: 'https://onurmicoogullari.github.io',
  baseUrl: process.env.DOCS_BASE_URL ?? '/azure-workload-identity-operator/',
  organizationName: 'onurmicoogullari',
  projectName: 'azure-workload-identity-operator',
  trailingSlash: true,

  onBrokenLinks: 'throw',
  markdown: {
    mermaid: true,
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },
  themes: ['@docusaurus/theme-mermaid'],

  presets: [
    [
      'classic',
      {
        docs: {
          path: 'content',
          routeBasePath: '/',
          sidebarPath: './sidebars.ts',
          editUrl: `${repositoryUrl}/edit/main/docs/`,
          showLastUpdateTime: true,
          breadcrumbs: true,
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
        sitemap: {
          changefreq: 'weekly',
          priority: 0.5,
          ignorePatterns: ['/tags/**'],
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    image: 'img/social-card.svg',
    metadata: [
      {
        name: 'keywords',
        content:
          'Kubernetes, OpenShift, Azure, workload identity, OIDC, operator',
      },
    ],
    colorMode: {
      defaultMode: 'light',
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'Workload Identity Operator',
      hideOnScroll: true,
      logo: {
        alt: 'Azure Workload Identity Operator',
        src: 'img/logo.svg',
        srcDark: 'img/logo-dark.svg',
      },
      items: [
        {
          type: 'doc',
          docId: 'getting-started/installation',
          position: 'left',
          label: 'Install',
        },
        {
          type: 'doc',
          docId: 'guides/workload-identity',
          position: 'left',
          label: 'Guides',
        },
        {
          type: 'doc',
          docId: 'architecture/overview',
          position: 'left',
          label: 'Architecture',
        },
        {
          type: 'doc',
          docId: 'reference/oidcissuer',
          position: 'left',
          label: 'Reference',
        },
        {
          href: repositoryUrl,
          label: 'GitHub',
          position: 'right',
          className: 'navbar-github-link',
        },
      ],
    },
    docs: {
      sidebar: {
        hideable: true,
        autoCollapseCategories: false,
      },
    },
    footer: {
      style: 'dark',
      copyright:
        'Azure Workload identity Operator by <a href="https://github.com/onurmicoogullari">onurmicoogullari</a> • 2026',
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'json', 'yaml'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
