#!/usr/bin/env node
import { lookup } from 'node:dns/promises';
import { isIP } from 'node:net';
import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

function usage() {
  console.error(`usage: record-page.mjs [--allow-unsafe-urls] <url> [output.mp4]

Options:
  --allow-unsafe-urls  Allow trusted local/private captures. Refused in CI.`);
}

let allowUnsafeURLs = false;
let url;
let outputArg;
for (const item of process.argv.slice(2)) {
  if (item === '--allow-unsafe-urls') allowUnsafeURLs = true;
  else if (item === '--help' || item === '-h') { usage(); process.exit(0); }
  else if (!url) url = item;
  else if (!outputArg) outputArg = item;
  else { console.error(`error: unexpected argument: ${item}`); process.exit(2); }
}
if (!url) {
  usage();
  process.exit(2);
}
if (allowUnsafeURLs && process.env.CI === 'true') {
  console.error('error: --allow-unsafe-urls is refused in CI');
  process.exit(2);
}

const output = resolve(outputArg ?? 'page.capture.mp4');
const chromeCandidates = [
  process.env.CHROMIUM_PATH,
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  `${process.env.HOME}/.cache/rod/browser/chromium-1321438/Chromium.app/Contents/MacOS/Chromium`,
];
const width = Number(process.env.DOCS_RECORD_WIDTH ?? 1440);
const height = Number(process.env.DOCS_RECORD_HEIGHT ?? 900);
const fps = Number(process.env.DOCS_RECORD_FPS ?? 10);
const seconds = Number(process.env.DOCS_RECORD_SECONDS ?? 24);
const totalFrames = fps * seconds;
const holdStartFrames = fps * 2;
const holdEndFrames = fps * 2;

async function sleep(ms) {
  await new Promise((resolve) => setTimeout(resolve, ms));
}

async function waitForJSON(endpoint, timeoutMs = 10000) {
  const start = Date.now();
  let lastError;
  while (Date.now() - start < timeoutMs) {
    try {
      const response = await fetch(endpoint);
      if (response.ok) return await response.json();
    } catch (error) {
      lastError = error;
    }
    await sleep(100);
  }
  throw new Error(`Timed out waiting for ${endpoint}: ${lastError?.message ?? 'no response'}`);
}

function connect(wsURL) {
  const ws = new WebSocket(wsURL);
  let nextID = 1;
  const pending = new Map();
  ws.addEventListener('message', (event) => {
    const msg = JSON.parse(event.data);
    if (msg.id && pending.has(msg.id)) {
      const { resolve, reject } = pending.get(msg.id);
      pending.delete(msg.id);
      if (msg.error) reject(new Error(JSON.stringify(msg.error)));
      else resolve(msg.result);
    }
  });
  return new Promise((resolve, reject) => {
    ws.addEventListener('open', () => {
      resolve({
        send(method, params = {}) {
          const id = nextID++;
          ws.send(JSON.stringify({ id, method, params }));
          return new Promise((resolve, reject) => pending.set(id, { resolve, reject }));
        },
        close() {
          ws.close();
        },
      });
    }, { once: true });
    ws.addEventListener('error', reject, { once: true });
  });
}

function easeInOut(t) {
  return t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2;
}

function normalizeHostname(hostname) {
  return hostname.replace(/^\[(.*)\]$/, '$1').toLowerCase();
}

function isBlockedIPv4(address) {
  const parts = address.split('.').map((part) => Number(part));
  if (parts.length !== 4 || parts.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) return true;
  const [a, b, c] = parts;
  return a === 0
    || a === 10
    || a === 127
    || (a === 100 && b >= 64 && b <= 127)
    || (a === 169 && b === 254)
    || (a === 172 && b >= 16 && b <= 31)
    || (a === 192 && (b === 168 || (b === 0 && (c === 0 || c === 2)) || (b === 88 && c === 99)))
    || (a === 198 && (b === 18 || b === 19 || (b === 51 && c === 100)))
    || (a === 203 && b === 0 && c === 113)
    || a >= 224;
}

function isBlockedIPv6(address) {
  const normalized = address.toLowerCase();
  if (normalized === '::' || normalized === '::1') return true;
  if (/^fe[89ab][0-9a-f]:/.test(normalized)) return true;
  if (normalized.startsWith('fc') || normalized.startsWith('fd') || normalized.startsWith('ff')) return true;
  const mapped = normalized.match(/^::ffff:(\d+\.\d+\.\d+\.\d+)$/);
  return mapped ? isBlockedIPv4(mapped[1]) : false;
}

function isBlockedIP(address) {
  const version = isIP(address);
  if (version === 4) return isBlockedIPv4(address);
  if (version === 6) return isBlockedIPv6(address);
  return true;
}

async function validateURL(input) {
  let parsed;
  try {
    parsed = new URL(input);
  } catch {
    throw new Error(`invalid URL: ${input}`);
  }

  if (allowUnsafeURLs) return parsed.href;

  if (parsed.protocol !== 'https:') {
    throw new Error('record-page only allows https URLs by default; pass --allow-unsafe-urls for trusted local captures');
  }
  if (parsed.username || parsed.password) {
    throw new Error('record-page URLs must not include embedded credentials');
  }

  const hostname = normalizeHostname(parsed.hostname);
  if (hostname === 'localhost' || hostname.endsWith('.localhost')) {
    throw new Error('record-page refuses localhost URLs by default');
  }

  if (isIP(hostname)) {
    if (isBlockedIP(hostname)) throw new Error(`record-page refuses private or reserved address: ${hostname}`);
    return parsed.href;
  }

  const addresses = await lookup(hostname, { all: true, verbatim: true });
  if (addresses.length === 0) throw new Error(`record-page could not resolve hostname: ${hostname}`);
  const blocked = addresses.find((entry) => isBlockedIP(entry.address));
  if (blocked) throw new Error(`record-page refuses hostname that resolves to private or reserved address: ${hostname} -> ${blocked.address}`);
  return parsed.href;
}

async function main() {
  const safeURL = await validateURL(url);
  const chromium = chromeCandidates.find((candidate) => candidate && existsSync(candidate));
  if (!chromium) throw new Error('set CHROMIUM_PATH to a Chrome or Chromium executable');

  const workdir = await mkdtemp(join(tmpdir(), 'screencast-docs-record-'));
  const userDataDir = join(workdir, 'profile');
  const framesDir = join(workdir, 'frames');
  await mkdir(framesDir, { recursive: true });

  const port = 9222 + Math.floor(Math.random() * 1000);
  const chrome = spawn(chromium, [
    '--headless=new',
    `--remote-debugging-port=${port}`,
    `--user-data-dir=${userDataDir}`,
    '--no-first-run',
    '--no-default-browser-check',
    '--disable-gpu',
    '--disable-background-networking',
    '--force-color-profile=srgb',
    `--window-size=${width},${height}`,
    'about:blank',
  ], { stdio: ['ignore', 'ignore', 'pipe'] });

  chrome.stderr.on('data', (chunk) => {
    const text = String(chunk);
    if (!text.includes('DevTools listening')) process.stderr.write(text);
  });

  try {
    await waitForJSON(`http://127.0.0.1:${port}/json/version`);
    await fetch(`http://127.0.0.1:${port}/json/new?${encodeURIComponent(safeURL)}`, { method: 'PUT' });
    const targets = await waitForJSON(`http://127.0.0.1:${port}/json/list`);
    const target = targets.find((t) => t.type === 'page' && t.url === safeURL)
      ?? targets.find((t) => t.type === 'page' && t.url !== 'about:blank')
      ?? targets.find((t) => t.type === 'page');
    if (!target?.webSocketDebuggerUrl) throw new Error('Could not find page target');

    const cdp = await connect(target.webSocketDebuggerUrl);
    await cdp.send('Page.enable');
    await cdp.send('Runtime.enable');
    await cdp.send('Emulation.setDeviceMetricsOverride', {
      width,
      height,
      deviceScaleFactor: 1,
      mobile: false,
    });
    await sleep(2500);
    await cdp.send('Runtime.evaluate', {
      expression: `document.fonts && document.fonts.ready`,
      awaitPromise: true,
    }).catch(() => {});
    await cdp.send('Runtime.evaluate', {
      expression: `document.documentElement.style.scrollBehavior = 'auto'; window.scrollTo(0, 0);`,
    });
    await sleep(500);

    const metrics = await cdp.send('Runtime.evaluate', {
      expression: `({
        scrollHeight: Math.max(document.documentElement.scrollHeight, document.body.scrollHeight),
        innerHeight: window.innerHeight
      })`,
      returnByValue: true,
    });
    const pageHeight = metrics.result.value.scrollHeight;
    const maxScroll = Math.max(0, pageHeight - height);
    const endScroll = Math.min(maxScroll, Math.floor(maxScroll * 0.88));

    for (let i = 0; i < totalFrames; i++) {
      let y;
      if (i < holdStartFrames) y = 0;
      else if (i >= totalFrames - holdEndFrames) y = endScroll;
      else {
        const t = (i - holdStartFrames) / (totalFrames - holdStartFrames - holdEndFrames - 1);
        y = Math.round(endScroll * easeInOut(t));
      }
      await cdp.send('Runtime.evaluate', { expression: `window.scrollTo(0, ${y});` });
      await sleep(25);
      const shot = await cdp.send('Page.captureScreenshot', { format: 'png', fromSurface: true });
      const framePath = join(framesDir, `frame_${String(i + 1).padStart(4, '0')}.png`);
      await writeFile(framePath, Buffer.from(shot.data, 'base64'));
      if ((i + 1) % fps === 0) console.log(`captured ${Math.round((i + 1) / fps)}s/${seconds}s`);
    }
    cdp.close();

    await mkdir(resolve(output, '..'), { recursive: true });
    await new Promise((resolvePromise, rejectPromise) => {
      const ffmpeg = spawn('ffmpeg', [
        '-y',
        '-framerate', String(fps),
        '-i', join(framesDir, 'frame_%04d.png'),
        '-c:v', 'libx264',
        '-pix_fmt', 'yuv420p',
        '-movflags', '+faststart',
        output,
      ], { stdio: ['ignore', 'inherit', 'inherit'] });
      ffmpeg.on('exit', (code) => code === 0 ? resolvePromise() : rejectPromise(new Error(`ffmpeg exited ${code}`)));
    });
    console.log(`wrote ${output}`);
  } finally {
    if (!chrome.killed) chrome.kill('SIGTERM');
    await new Promise((resolve) => {
      chrome.once('exit', resolve);
      setTimeout(resolve, 1500);
    });
    await rm(workdir, { recursive: true, force: true, maxRetries: 3, retryDelay: 200 }).catch(() => {});
  }
}

main().catch((error) => {
  console.error(error.message ?? error);
  process.exit(1);
});
