import http from 'node:http';
import { readFile } from 'node:fs/promises';
import { resolve, extname, sep } from 'node:path';

const root = resolve(process.env.CORE_PACKAGE_ROOT ?? '.');
const tests = resolve('tests');
const packageJSON = JSON.parse(await readFile(resolve(root, 'package.json'), 'utf8'));
const browserEntry = packageJSON.exports['.'].browser.replace(/^\.\//, '/');
const mime = { '.mjs': 'text/javascript', '.html': 'text/html', '.wasm': 'application/wasm' };
http.createServer(async (request, response) => {
  try {
    const pathname = new URL(request.url, 'http://localhost').pathname;
    const base = pathname.startsWith('/tests/') ? tests : root;
    const relative = base === tests ? pathname.slice('/tests'.length) : pathname;
    const path = resolve(base, `.${relative}`);
    if (!path.startsWith(base + sep)) throw new Error('Outside package');
    let content = await readFile(path);
    if (pathname === '/tests/browser.html') {
      content = Buffer.from(content.toString().replace('__CORE_BROWSER_ENTRY__', browserEntry));
    }
    response.writeHead(200, { 'Content-Type': mime[extname(path)] ?? 'application/octet-stream' });
    response.end(content);
  } catch {
    response.writeHead(404).end();
  }
}).listen(4173, '127.0.0.1');
