import { mkdir, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const docsRoot = path.resolve(fileURLToPath(new URL('..', import.meta.url)));
const contentRoot = path.join(docsRoot, 'content', 'docs');
const publicRoot = path.join(docsRoot, 'public');
const skipDirs = new Set([
  '.git',
  '.next',
  '.source',
  'app',
  'components',
  'content',
  'export',
  'lib',
  'node_modules',
  'out',
  'public',
  'scripts',
]);
const publicAssetExtensions = new Set([
  '.gif',
  '.ico',
  '.jpeg',
  '.jpg',
  '.json',
  '.pdf',
  '.png',
  '.svg',
  '.txt',
  '.webp',
  '.zip',
]);
const appFiles = new Set(['package-lock.json', 'package.json', 'tsconfig.json']);

await rm(contentRoot, { recursive: true, force: true });
await rm(publicRoot, { recursive: true, force: true });
await mkdir(contentRoot, { recursive: true });
await mkdir(publicRoot, { recursive: true });

const markdownFiles = [];
const assetFiles = [];
const routeDirsBySourceDir = new Map();

async function walk(dir) {
  const entries = await readdir(dir, { withFileTypes: true });

  for (const entry of entries) {
    if (entry.name.startsWith('.') && entry.name !== '.gitignore') continue;
    if (entry.isDirectory() && skipDirs.has(entry.name)) continue;

    const absolutePath = path.join(dir, entry.name);
    const relativePath = path.relative(docsRoot, absolutePath);

    if (entry.isDirectory()) {
      await walk(absolutePath);
    } else if (entry.isFile() && entry.name.endsWith('.md')) {
      markdownFiles.push({ absolutePath, relativePath });
    } else if (entry.isFile() && shouldPublishAsset(relativePath)) {
      assetFiles.push({ absolutePath, relativePath });
    }
  }
}

await walk(docsRoot);

for (const file of markdownFiles) {
  const targetRelativePath = normalizeMarkdownPath(file.relativePath);
  const targetPath = path.join(contentRoot, targetRelativePath);
  const raw = normalizeMarkdown(await readFile(file.absolutePath, 'utf8'));

  addRouteDirForSource(file.relativePath, targetRelativePath);
  await mkdir(path.dirname(targetPath), { recursive: true });
  await writeFile(targetPath, withFrontmatter(raw, targetRelativePath), 'utf8');
}

await writeIndexPage();

for (const file of assetFiles) {
  const raw = await readFile(file.absolutePath);

  await writePublicAsset(path.join(publicRoot, file.relativePath), raw);

  for (const routeDir of routeDirsBySourceDir.get(path.dirname(file.relativePath)) ?? []) {
    await writePublicAsset(path.join(publicRoot, routeDir, path.basename(file.relativePath)), raw);
  }
}

function normalizeMarkdownPath(relativePath) {
  if (path.basename(relativePath).toLowerCase() === 'readme.md') {
    return path.join(path.dirname(relativePath), 'index.md');
  }

  return relativePath;
}

function addRouteDirForSource(sourceRelativePath, targetRelativePath) {
  const sourceDir = path.dirname(sourceRelativePath);
  const routeDir =
    path.basename(targetRelativePath) === 'index.md'
      ? path.dirname(targetRelativePath)
      : targetRelativePath.slice(0, -path.extname(targetRelativePath).length);

  if (routeDir === '.') return;

  const routeDirs = routeDirsBySourceDir.get(sourceDir) ?? new Set();
  routeDirs.add(routeDir);
  routeDirsBySourceDir.set(sourceDir, routeDirs);
}

async function writePublicAsset(targetPath, raw) {
  await mkdir(path.dirname(targetPath), { recursive: true });
  await writeFile(targetPath, raw);
}

function shouldPublishAsset(relativePath) {
  const extension = path.extname(relativePath).toLowerCase();

  if (appFiles.has(relativePath)) return false;

  return publicAssetExtensions.has(extension);
}

function withFrontmatter(raw, relativePath) {
  if (raw.trimStart().startsWith('---')) return raw;

  const title = inferTitle(raw, relativePath);
  const description = inferDescription(raw);

  return `---\ntitle: ${JSON.stringify(title)}\ndescription: ${JSON.stringify(description)}\n---\n\n${raw}`;
}

function normalizeMarkdown(raw) {
  return raw.replace(/^```bitbake\b/gm, '```ini');
}

function inferTitle(raw, relativePath) {
  const heading = raw.match(/^#\s+(.+)$/m)?.[1]?.trim();
  if (heading) return stripMarkdown(heading);

  const basename = path.basename(relativePath, path.extname(relativePath));
  const dirname = path.basename(path.dirname(relativePath));
  const name = basename === 'index' ? dirname : basename;

  return toTitle(name);
}

function inferDescription(raw) {
  const withoutCode = raw.replace(/```[\s\S]*?```/g, '');
  const paragraph = withoutCode
    .split(/\n{2,}/)
    .map((part) => part.trim())
    .find((part) => part && !part.startsWith('#') && !part.startsWith('|') && !part.startsWith('---'));

  if (!paragraph) return 'WendyOS developer documentation.';

  return stripMarkdown(paragraph).replace(/\s+/g, ' ').slice(0, 180);
}

function stripMarkdown(value) {
  return value
    .replace(/\[([^\]]+)\]\([^)]+\)/g, '$1')
    .replace(/[`*_>#]/g, '')
    .trim();
}

function toTitle(value) {
  return value
    .replace(/[-_]+/g, ' ')
    .replace(/\b\w/g, (char) => char.toUpperCase())
    .replace(/\bCli\b/g, 'CLI')
    .replace(/\bGpu\b/g, 'GPU')
    .replace(/\bGrpc\b/g, 'gRPC')
    .replace(/\bMcp\b/g, 'MCP')
    .replace(/\bOci\b/g, 'OCI')
    .replace(/\bPki\b/g, 'PKI');
}

async function writeIndexPage() {
  const targetPath = path.join(contentRoot, 'index.md');
  const body = `---\ntitle: "WendyOS Docs"\ndescription: "Developer documentation for WendyOS, wendy-agent, Wendy Cloud, and the Wendy CLI."\n---\n\n# WendyOS Docs\n\nWendyOS is the edge-device operating system and runtime platform for building, deploying, and debugging apps on Raspberry Pi, NVIDIA Jetson, x86 SBCs, and more.\n\n## Start Here\n\n- [WendyOS](./wendyos/requirements.md)\n- [wendy-agent](./wendy-agent/architecture.md)\n- [Wendy CLI](./clients/wendy-cli/global-flags.md)\n- [App configuration](./apps/wendy.json.md)\n- [Wendy Cloud](./cloud/requirements.md)\n- [Development](./development/)\n`;

  await writeFile(targetPath, body, 'utf8');
}
