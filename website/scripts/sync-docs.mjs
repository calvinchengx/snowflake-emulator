// Generates Starlight content from the canonical Markdown in /docs.
import { readdirSync, readFileSync, writeFileSync, rmSync, mkdirSync, existsSync, statSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { collectParity, writeParityHistory, parityManifest } from './parity-versions.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = join(here, '..', '..');
const DOCS_SRC = join(REPO_ROOT, 'docs');
const OUT = join(here, '..', 'src', 'content', 'docs');
export { BASE } from '../base.mjs';
import { BASE } from '../base.mjs';
const REPO = 'https://github.com/calvinchengx/snowflake-emulator';

const PARITY = collectParity(REPO_ROOT);
const IS_RELEASE = /^v\d+\.\d+\.\d+$/.test(PARITY.version);
const PARITY_RE = /(^|\/)parity\.md$/;

const DOC_RE = /^(\d{2}-[a-z0-9-]+|parity)\.md$/;
const LINK_RE = /\]\((?:\.\/|docs\/)?(\d{2}-[a-z0-9-]+|parity)\.md(#[^)]*)?\)/g;
const REPO_LINK_RE = /\]\(\.\.\/([^)#]+)(#[^)]*)?\)/g;

function rewriteRepoLinks(md, where) {
  return md.replace(REPO_LINK_RE, (_m, path, anchor) => {
    const clean = path.replace(/\/+$/, '');
    const target = join(REPO_ROOT, clean);
    const exists = existsSync(target);
    if (!exists) {
      console.warn(`sync-docs: WARNING ${where}: ../${path} matches nothing in the repo`);
    }
    const kind = exists && statSync(target).isDirectory() ? 'tree' : 'blob';
    return `](${REPO}/${kind}/main/${clean}${anchor ?? ''})`;
  });
}

function rewriteLinks(md, where = 'docs') {
  return rewriteRepoLinks(
    md
      .replace(/\]\(witnesses\.json\)/g, `](${REPO}/blob/main/docs/witnesses.json)`)
      .replace(LINK_RE, (_m, slug, anchor) => `](${BASE}${slug}/${anchor ?? ''})`),
    where,
  );
}

function cleanTitle(h1) {
  return h1.replace(/^\d+[a-z]?\s*[—:-]\s*/i, '').trim();
}

function yamlEscape(s) {
  return '"' + s.replace(/\\/g, '\\\\').replace(/"/g, '\\"') + '"';
}

function convertBody(raw, where = 'docs') {
  const lines = raw.split('\n');
  const h1Index = lines.findIndex((l) => /^#\s+/.test(l));
  if (h1Index >= 0) {
    lines.splice(h1Index, lines[h1Index + 1]?.trim() === '' ? 2 : 1);
  }
  return rewriteLinks(lines.join('\n').replace(/^\n+/, ''), where);
}

function parityStamp() {
  const what = IS_RELEASE
    ? `release **${PARITY.version}**`
    : `**${PARITY.version}** (the live tip of \`main\`)`;
  return (
    `_Parity map as of ${what} — tracked by git release tags. ` +
    `See the [version history](${BASE}parity-history/) and [parity changelog](${BASE}parity-history/changelog/)._\n\n`
  );
}

function convert(srcPath, name) {
  const raw = readFileSync(srcPath, 'utf8');
  const h1 = raw.split('\n').find((l) => /^#\s+/.test(l));
  const title = h1 ? cleanTitle(h1.replace(/^#\s+/, '')) : name.replace(/\.md$/, '');
  let body = convertBody(raw, name);
  if (PARITY_RE.test(name)) body = parityStamp() + body;
  const editUrl = `${REPO}/edit/main/docs/${name}`;
  return `---\ntitle: ${yamlEscape(title)}\neditUrl: ${yamlEscape(editUrl)}\n---\n\n` + body;
}

function writeIndex() {
  // Render the repo's own docs/index.md rather than a hardcoded landing page.
  //
  // This file was copied from databricks-emulator and its writeIndex() kept
  // that repo's title, description, prose and chapter list, so the published
  // home page announced a "Databricks workspace" and linked to pages that do
  // not exist here. Reading docs/index.md means the landing page cannot drift
  // from the repo again: there is only one copy of it.
  const src = join(DOCS_SRC, 'index.md');
  const raw = readFileSync(src, 'utf8');
  const match = /^---\n([\s\S]*?)\n---\n?/.exec(raw);
  if (!match) {
    throw new Error('sync-docs: docs/index.md needs frontmatter with a title');
  }
  const frontmatter = match[1].replace(/\neditUrl:.*$/m, '');
  const body = rewriteLinks(rewriteRepoLinks(raw.slice(match[0].length), 'index'), 'index');
  writeFileSync(
    join(OUT, 'index.md'),
    `---\n${frontmatter}\neditUrl: ${JSON.stringify(`${REPO}/edit/main/docs/index.md`)}\n---\n\n` + body,
  );
}

rmSync(OUT, { recursive: true, force: true });
mkdirSync(OUT, { recursive: true });

const names = readdirSync(DOCS_SRC).filter((n) => DOC_RE.test(n)).sort();
for (const name of names) {
  writeFileSync(join(OUT, name), convert(join(DOCS_SRC, name), name));
}
writeIndex();
const info = writeParityHistory(OUT, PARITY, { convertBody });
const DATA = join(here, '..', 'src', 'data');
mkdirSync(DATA, { recursive: true });
writeFileSync(join(DATA, 'parity-versions.json'), JSON.stringify(parityManifest(PARITY), null, 2) + '\n');
console.log(
  `sync-docs: wrote ${names.length} docs + index to src/content/docs/ ` +
    `(parity ${info.version}; ${info.snapshots.length} tagged snapshot(s))`,
);
