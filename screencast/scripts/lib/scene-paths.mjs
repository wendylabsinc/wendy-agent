import { existsSync } from 'node:fs';
import { readdir } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';

export const screencastRoot = resolve(new URL('../..', import.meta.url).pathname);

export function pathExists(path) {
  return existsSync(path);
}

async function scenePrefixMatch(root, token) {
  const scenesDir = join(root, 'scenes');
  if (!existsSync(scenesDir) || token.includes('/')) return null;
  const entries = await readdir(scenesDir, { withFileTypes: true });
  const matches = entries
    .filter((entry) => entry.isDirectory() && !entry.name.startsWith('.') && entry.name.startsWith(token))
    .map((entry) => entry.name)
    .sort();
  if (matches.length === 1) return join(scenesDir, matches[0]);
  if (matches.length > 1) {
    throw new Error(`ambiguous scene prefix '${token}': ${matches.join(', ')}`);
  }
  return null;
}

async function resolveInput(root, arg) {
  const candidates = [];
  if (arg.startsWith('/')) candidates.push(resolve(arg));
  else {
    candidates.push(resolve(process.cwd(), arg));
    candidates.push(resolve(root, arg));
    candidates.push(resolve(root, 'scenes', arg));
  }
  for (const candidate of candidates) {
    if (existsSync(candidate)) return candidate;
  }
  const scene = await scenePrefixMatch(root, arg);
  if (scene) return scene;
  throw new Error(`not found: ${arg}`);
}

export async function resolveSceneFile(arg, defaultFilename, root = screencastRoot) {
  const input = await resolveInput(root, arg);
  const stat = await import('node:fs/promises').then(({ stat }) => stat(input));
  const source = stat.isDirectory() ? join(input, defaultFilename) : input;
  if (!existsSync(source)) throw new Error(`missing ${defaultFilename}: ${source}`);
  return {
    sceneDir: stat.isDirectory() ? input : dirname(input),
    source,
  };
}

export async function sceneDirectories(root = screencastRoot) {
  const scenesDir = join(root, 'scenes');
  if (!existsSync(scenesDir)) throw new Error(`no scenes directory: ${scenesDir}`);
  const entries = await readdir(scenesDir, { withFileTypes: true });
  return entries
    .filter((entry) => entry.isDirectory() && !entry.name.startsWith('.'))
    .map((entry) => join(scenesDir, entry.name))
    .sort();
}

export function resolveRoot(arg, root = screencastRoot) {
  if (!arg) return root;
  if (arg.startsWith('/')) return resolve(arg);
  const cwdPath = resolve(process.cwd(), arg);
  if (existsSync(cwdPath)) return cwdPath;
  return resolve(root, arg);
}
