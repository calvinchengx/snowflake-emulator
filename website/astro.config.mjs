import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import { remarkMermaid } from './plugins/remark-mermaid.mjs';
import { BASE } from './base.mjs';

export default defineConfig({
  site: 'https://calvinchengx.github.io',
  // The docs moved down a level so the root can be a landing page.
  // See base.mjs -- three files need this string and they must agree.
  base: BASE,
  markdown: {
    remarkPlugins: [remarkMermaid],
  },
  integrations: [
    starlight({
      title: 'Snowflake Emulator',
      components: {
        Head: './src/components/Head.astro',
        Search: './src/components/Search.astro',
        // A back-link to the landing page beside the site title. The component
        // explains why it cannot live in the header icon row or the sidebar.
        SiteTitle: './src/components/SiteTitle.astro',
      },
      description:
        'A local Snowflake account emulator — official connectors on localhost, SQL on DuckDB, and everything else refused by name rather than quietly ignored.',
      social: [
        { icon: 'github', label: 'GitHub', href: 'https://github.com/calvinchengx/snowflake-emulator' },
      ],
      editLink: {
        baseUrl: 'https://github.com/calvinchengx/snowflake-emulator/edit/main/docs/',
      },
      sidebar: [
        {
          label: 'Getting started',
          items: [
            { slug: 'index' },
            { slug: '00-doctrine' },
            { slug: '01-quickstart' },
            { slug: '02-installation' },
            { slug: '03-architecture' },
            { slug: '04-configuration' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { slug: '05-sql-surface' },
            { slug: '06-stages-and-copy' },
            { slug: '07-semi-structured' },
            { slug: '08-tasks-and-streams' },
            { slug: '09-clients' },
          ],
        },
        {
          label: 'The project',
          items: [
            { slug: '10-testing' },
            { slug: '11-family-integration' },
            { slug: '12-roadmap' },
          ],
        },
        {
          label: 'Parity',
          items: [
            { slug: 'parity', label: 'Parity ledger' },
            { slug: 'parity-history' },
            { slug: 'parity-history/changelog' },
          ],
        },
      ],
    }),
  ],
});
