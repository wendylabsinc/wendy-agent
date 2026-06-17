#!/usr/bin/env node
import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

const url = process.argv[2];
if (!url) {
  console.error('usage: record-docs-page.mjs <url> [output.mp4]');
  process.exit(2);
}

const output = resolve(process.argv[3] ?? 'deck/public/videos/docs-page.mp4');
const chromium = process.env.CHROMIUM_PATH ?? [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  `${process.env.HOME}/.cache/rod/browser/chromium-1321438/Chromium.app/Contents/MacOS/Chromium`,
].find((candidate) => candidate && existsSync(candidate));
if (!chromium) {
  console.error('error: set CHROMIUM_PATH to a Chrome or Chromium executable');
  process.exit(2);
}
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

async function main() {
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
    await fetch(`http://127.0.0.1:${port}/json/new?${encodeURIComponent(url)}`, { method: 'PUT' });
    const targets = await waitForJSON(`http://127.0.0.1:${port}/json/list`);
    const target = targets.find((t) => t.type === 'page' && t.url === url)
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
  console.error(error);
  process.exit(1);
});
