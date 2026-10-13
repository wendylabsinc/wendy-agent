import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { createCore } from './interface.mjs';

const target = `${process.platform}-${process.arch}`;
const library = {
  darwin: 'libWendyCore.dylib',
  linux: 'libWendyCore.so',
  win32: 'WendyCore.dll',
}[process.platform];
if (!library) throw new Error(`Wendy Core does not support ${target}`);
const directory = new URL(`../assets/native/${target}/`, import.meta.url);
const require = createRequire(import.meta.url);
const addon = require(fileURLToPath(new URL('wendy_core.node', directory)));
const hello = addon.open(fileURLToPath(new URL(library, directory)));
const core = createCore({ WendyHelloWorld: hello });
export const WendyHelloWorld = core.WendyHelloWorld;
