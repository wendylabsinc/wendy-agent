// Executed verbatim in Node.js and an actual browser against the real engine.
export function assertCore(core) {
  const exports = Object.keys(core).sort();
  if (JSON.stringify(exports) !== '["WendyHelloWorld"]') {
    throw new Error(`Unexpected Core exports: ${exports}`);
  }
  for (let i = 0; i < 100; i++) {
    const value = core.WendyHelloWorld();
    if (value !== 42 || !Number.isInteger(value)) {
      throw new Error(`WendyHelloWorld returned ${value}`);
    }
  }
  return true;
}
