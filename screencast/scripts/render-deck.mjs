#!/usr/bin/env node
import { spawn, spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';

const projectDir = resolve(new URL('..', import.meta.url).pathname);
const timelinePath = resolve(process.argv[2] ?? join(projectDir, 'timeline.json'));
const timeline = JSON.parse(await (await import('node:fs/promises')).readFile(timelinePath, 'utf8'));
const width = Number(process.env.SCREENCAST_WIDTH ?? timeline.size?.width ?? 1440);
const height = Number(process.env.SCREENCAST_HEIGHT ?? timeline.size?.height ?? 900);
const fps = Number(process.env.SCREENCAST_FPS ?? timeline.size?.fps ?? 10);
const crf = process.env.SCREENCAST_CRF ?? '18';
const outFile = resolve(process.env.OUT_FILE ?? join(projectDir, 'output/screencast.mp4'));
const chromePath = process.env.CHROMIUM_PATH ?? [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Chromium.app/Contents/MacOS/Chromium',
  `${process.env.HOME}/.cache/rod/browser/chromium-1321438/Chromium.app/Contents/MacOS/Chromium`,
].find((candidate) => candidate && existsSync(candidate));

if (!chromePath) {
  console.error('error: set CHROMIUM_PATH to a Chrome or Chromium executable');
  process.exit(2);
}

function checkTool(name) {
  const result = spawnSync('which', [name], { stdio: 'ignore' });
  if (result.status !== 0) {
    console.error(`error: required tool not found: ${name}`);
    process.exit(2);
  }
}

checkTool('ffmpeg');
checkTool('ffprobe');
checkTool('npx');

function ffprobeDuration(path) {
  if (!path || !existsSync(path)) return 0;
  const result = spawnSync('ffprobe', [
    '-v', 'error',
    '-show_entries', 'format=duration',
    '-of', 'default=nk=1:nw=1',
    path,
  ], { encoding: 'utf8' });
  if (result.status !== 0) return 0;
  return Number(result.stdout.trim()) || 0;
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { stdio: 'inherit', ...options });
  if (result.status !== 0) throw new Error(`${command} exited ${result.status}`);
}

function concatLine(path) {
  return `file '${resolve(path).replaceAll("'", "'\\''")}'\n`;
}

async function sleep(ms) {
  await new Promise((resolvePromise) => setTimeout(resolvePromise, ms));
}

async function waitForHTTP(url, timeoutMs = 30000) {
  const start = Date.now();
  let lastError;
  while (Date.now() - start < timeoutMs) {
    try {
      const response = await fetch(url);
      if (response.ok) return;
    } catch (error) {
      lastError = error;
    }
    await sleep(250);
  }
  throw new Error(`Timed out waiting for ${url}: ${lastError?.message ?? 'no response'}`);
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
      const { resolve: resolvePromise, reject } = pending.get(msg.id);
      pending.delete(msg.id);
      if (msg.error) reject(new Error(JSON.stringify(msg.error)));
      else resolvePromise(msg.result);
    }
  });
  return new Promise((resolvePromise, reject) => {
    ws.addEventListener('open', () => {
      resolvePromise({
        send(method, params = {}) {
          const id = nextID++;
          ws.send(JSON.stringify({ id, method, params }));
          return new Promise((resolveRequest, rejectRequest) => pending.set(id, { resolve: resolveRequest, reject: rejectRequest }));
        },
        close() { ws.close(); },
      });
    }, { once: true });
    ws.addEventListener('error', reject, { once: true });
  });
}

async function captureSlideStill(cdp, slidevBaseURL, target, output) {
  await cdp.send('Page.navigate', { url: `${slidevBaseURL}/${target}` });
  for (let attempt = 0; attempt < 40; attempt += 1) {
    const result = await cdp.send('Runtime.evaluate', {
      expression: `(() => {
        const text = document.body?.innerText?.trim() ?? '';
        const slide = document.querySelector('.slidev-layout, .slidev-page');
        return Boolean(slide) && text.length > 0 && !text.includes('Loading');
      })()`,
      returnByValue: true,
    }).catch(() => ({ result: { value: false } }));
    if (result.result?.value) break;
    await sleep(200);
  }
  await sleep(300);
  await cdp.send('Runtime.evaluate', {
    expression: `document.fonts && document.fonts.ready`,
    awaitPromise: true,
  }).catch(() => {});
  await cdp.send('Runtime.evaluate', {
    expression: `for (const video of document.querySelectorAll('video')) { video.controls = false; video.pause(); }`,
  }).catch(() => {});
  const shot = await cdp.send('Page.captureScreenshot', { format: 'png', fromSurface: true });
  await writeFile(output, Buffer.from(shot.data, 'base64'));
}

function renderStillVideo(png, seconds, output) {
  run('ffmpeg', [
    '-nostdin', '-y', '-loop', '1', '-i', png,
    '-t', String(seconds),
    '-vf', `fps=${fps},scale=${width}:${height}:flags=lanczos,setsar=1,format=yuv420p`,
    '-an', '-c:v', 'libx264', '-preset', 'medium', '-crf', crf,
    '-pix_fmt', 'yuv420p', '-movflags', '+faststart', output,
  ]);
}

function renderMediaVideo(media, seconds, output) {
  run('ffmpeg', [
    '-nostdin', '-y', '-i', media,
    '-vf', `scale=${width}:${height}:force_original_aspect_ratio=decrease:flags=lanczos,pad=${width}:${height}:(ow-iw)/2:(oh-ih)/2:color=black,fps=${fps},setsar=1,format=yuv420p,tpad=stop_mode=clone:stop_duration=${seconds},trim=duration=${seconds},setpts=PTS-STARTPTS`,
    '-an', '-c:v', 'libx264', '-preset', 'medium', '-crf', crf,
    '-pix_fmt', 'yuv420p', '-movflags', '+faststart', output,
  ]);
}

function renderAudio(voicePath, seconds, output) {
  if (voicePath && existsSync(voicePath)) {
    run('ffmpeg', [
      '-nostdin', '-y', '-i', voicePath,
      '-af', `apad=pad_dur=${seconds},atrim=0:${seconds},asetpts=PTS-STARTPTS`,
      '-ar', '48000', '-ac', '2', '-c:a', 'pcm_s16le', output,
    ]);
  } else {
    run('ffmpeg', [
      '-nostdin', '-y', '-f', 'lavfi', '-i', 'anullsrc=channel_layout=stereo:sample_rate=48000',
      '-t', String(seconds), '-c:a', 'pcm_s16le', output,
    ]);
  }
}

async function main() {
  const workdir = await mkdtemp(join(tmpdir(), 'screencast-render-'));
  const stillDir = join(workdir, 'stills');
  const videoDir = join(workdir, 'video');
  const audioDir = join(workdir, 'audio');
  const userDataDir = join(workdir, 'chrome-profile');
  await mkdir(stillDir, { recursive: true });
  await mkdir(videoDir, { recursive: true });
  await mkdir(audioDir, { recursive: true });
  await mkdir(dirname(outFile), { recursive: true });

  const slidevPort = Number(process.env.SLIDEV_PORT ?? 3030);
  const chromePort = 9222 + Math.floor(Math.random() * 1000);
  const slidevBaseURL = `http://localhost:${slidevPort}`;

  const slidev = spawn('npx', ['slidev', timeline.deck, '--port', String(slidevPort)], {
    cwd: projectDir,
    stdio: ['ignore', 'inherit', 'inherit'],
  });
  const chrome = spawn(chromePath, [
    '--headless=new',
    `--remote-debugging-port=${chromePort}`,
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

  let cdp;
  try {
    await waitForHTTP(`${slidevBaseURL}/`);
    await waitForJSON(`http://127.0.0.1:${chromePort}/json/version`);
    await fetch(`http://127.0.0.1:${chromePort}/json/new?${encodeURIComponent(`${slidevBaseURL}/1`)}`, { method: 'PUT' });
    const targets = await waitForJSON(`http://127.0.0.1:${chromePort}/json/list`);
    const target = targets.find((t) => t.type === 'page' && t.url.startsWith(slidevBaseURL))
      ?? targets.find((t) => t.type === 'page');
    if (!target?.webSocketDebuggerUrl) throw new Error('Could not find page target');

    cdp = await connect(target.webSocketDebuggerUrl);
    await cdp.send('Page.enable');
    await cdp.send('Runtime.enable');
    await cdp.send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile: false });

    const videoList = [];
    const audioList = [];
    const report = ['id\ttarget\tseconds\tvoiceover_seconds\tmedia_seconds\tvisual_source'];

    for (const [index, step] of timeline.steps.entries()) {
      const n = String(index + 1).padStart(3, '0');
      const voicePath = step.voiceover ? resolve(projectDir, step.voiceover) : null;
      const mediaPath = step.media ? resolve(projectDir, step.media) : null;
      const voiceSeconds = ffprobeDuration(voicePath);
      const mediaSeconds = ffprobeDuration(mediaPath);
      const seconds = Math.max(Number(step.minSeconds ?? 0), voiceSeconds, mediaSeconds);
      const videoOut = join(videoDir, `${n}-${step.id}.mp4`);
      const audioOut = join(audioDir, `${n}-${step.id}.wav`);
      if (mediaPath && !existsSync(mediaPath)) {
        throw new Error(`timeline media is missing for ${step.id}: ${step.media}`);
      }
      const hasMedia = Boolean(mediaPath);
      const visualSource = hasMedia ? step.media : `slide:${step.target}`;

      console.log(`Rendering ${step.id}: ${visualSource}, ${seconds.toFixed(3)}s`);
      if (hasMedia) {
        renderMediaVideo(mediaPath, seconds, videoOut);
      } else {
        const still = join(stillDir, `${n}-${step.id}.png`);
        await captureSlideStill(cdp, slidevBaseURL, step.target, still);
        renderStillVideo(still, seconds, videoOut);
      }
      renderAudio(voicePath, seconds, audioOut);
      videoList.push(videoOut);
      audioList.push(audioOut);
      report.push(`${step.id}\t${step.target}\t${seconds.toFixed(3)}\t${voiceSeconds.toFixed(3)}\t${mediaSeconds.toFixed(3)}\t${visualSource}`);
    }

    await writeFile(join(workdir, 'video-list.txt'), videoList.map(concatLine).join(''));
    await writeFile(join(workdir, 'audio-list.txt'), audioList.map(concatLine).join(''));
    await writeFile(join(projectDir, 'output/duration-report.tsv'), `${report.join('\n')}\n`);

    const videoFull = join(workdir, 'video-full.mp4');
    const audioFull = join(workdir, 'audio-full.wav');
    run('ffmpeg', ['-nostdin', '-y', '-f', 'concat', '-safe', '0', '-i', join(workdir, 'video-list.txt'), '-c', 'copy', videoFull]);
    run('ffmpeg', ['-nostdin', '-y', '-f', 'concat', '-safe', '0', '-i', join(workdir, 'audio-list.txt'), '-c:a', 'pcm_s16le', audioFull]);
    run('ffmpeg', [
      '-nostdin', '-y', '-i', videoFull, '-i', audioFull,
      '-map', '0:v:0', '-map', '1:a:0', '-c:v', 'copy', '-c:a', 'aac', '-b:a', '192k',
      '-shortest', '-movflags', '+faststart', outFile,
    ]);

    console.log(`wrote ${outFile}`);
    run('ffprobe', ['-v', 'error', '-show_entries', 'format=duration', '-of', 'default=nk=1:nw=1', outFile]);
    run('ffprobe', ['-v', 'error', '-select_streams', 'v:0', '-show_entries', 'stream=width,height,r_frame_rate', '-of', 'csv=p=0', outFile]);
  } finally {
    cdp?.close();
    if (!chrome.killed) chrome.kill('SIGTERM');
    if (!slidev.killed) slidev.kill('SIGTERM');
    await sleep(1000);
    await rm(workdir, { recursive: true, force: true, maxRetries: 3, retryDelay: 200 }).catch(() => {});
  }
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
