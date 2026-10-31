const { spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');

const root = path.resolve(__dirname, '..');
const apiKey = 'emberdeck-e2e-test-key';

function portAvailable() {
  return new Promise((resolve, reject) => {
    const listener = net.createServer();
    listener.once('error', reject);
    listener.listen(0, '127.0.0.1', () => {
      const { port } = listener.address();
      listener.close((error) => error ? reject(error) : resolve(port));
    });
  });
}

function runTests(baseURL, openBaseURL) {
  return new Promise((resolve, reject) => {
    const cli = require.resolve('@playwright/test/cli');
    const child = spawn(process.execPath, [cli, 'test', '--config', 'e2e/playwright.config.js'], {
      cwd: root,
      stdio: 'inherit',
      env: { ...process.env, EMBERDECK_E2E_BASE_URL: baseURL, EMBERDECK_E2E_OPEN_BASE_URL: openBaseURL },
    });
    child.once('error', reject);
    child.once('exit', (code, signal) => {
      if (signal) reject(new Error(`Playwright exited from ${signal}`));
      else resolve(code ?? 1);
    });
  });
}

async function waitForServer(url, child, log) {
  const deadline = Date.now() + 20000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`EmberDeck exited early (${child.exitCode}).\n${log()}`);
    try {
      const response = await fetch(url);
      if (response.ok) return;
    } catch (_) {
      // The listener is not ready yet.
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`EmberDeck did not start at ${url}.\n${log()}`);
}

async function stopServer(child) {
  if (child.exitCode !== null) return;
  const exited = new Promise((resolve) => child.once('exit', resolve));
  child.kill('SIGTERM');
  const timeout = setTimeout(() => child.kill('SIGKILL'), 10000);
  try { await exited; } finally { clearTimeout(timeout); }
}

function startServer(binary, port, env) {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'emberdeck-browser-e2e-'));
  const child = spawn(binary, ['serve', '-listen', `127.0.0.1:${port}`, '-data', dataDir, '-simulate'], {
    cwd: root,
    stdio: ['ignore', 'pipe', 'pipe'],
    env,
  });
  let output = '';
  const remember = (chunk) => {
    output = (output + chunk.toString()).slice(-12000);
    process.stderr.write(chunk);
  };
  child.stdout.on('data', remember);
  child.stderr.on('data', remember);
  return { child, dataDir, log: () => output };
}

async function main() {
  const binary = path.resolve(process.env.EMBERDECK_BINARY || path.join(root, 'bin', process.platform === 'win32' ? 'emberdeck.exe' : 'emberdeck'));
  if (!process.env.EMBERDECK_BINARY) {
    fs.mkdirSync(path.dirname(binary), { recursive: true });
    const result = spawnSync('go', ['build', '-o', binary, './cmd/emberdeck'], { cwd: root, stdio: 'inherit' });
    if (result.status !== 0) throw new Error('Could not build EmberDeck for browser tests');
  }
  const requestedPort = process.env.EMBERDECK_E2E_PORT;
  const port = requestedPort ? Number(requestedPort) : await portAvailable();
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('EMBERDECK_E2E_PORT must be a TCP port from 1 through 65535');
  let openPort = await portAvailable();
  while (openPort === port) openPort = await portAvailable();
  const baseURL = `http://127.0.0.1:${port}`;
  const openBaseURL = `http://127.0.0.1:${openPort}`;
  const keyed = startServer(binary, port, { ...process.env, EMBERDECK_API_KEY: apiKey });
  const openEnv = { ...process.env };
  delete openEnv.EMBERDECK_API_KEY;
  delete openEnv.EMBERDECK_API_KEY_FILE;
  const open = startServer(binary, openPort, openEnv);
  try {
    await Promise.all([waitForServer(baseURL, keyed.child, keyed.log), waitForServer(openBaseURL, open.child, open.log)]);
    const exitCode = await runTests(baseURL, openBaseURL);
    if (exitCode !== 0) process.exitCode = exitCode;
  } finally {
    await Promise.all([stopServer(keyed.child), stopServer(open.child)]);
    fs.rmSync(keyed.dataDir, { recursive: true, force: true });
    fs.rmSync(open.dataDir, { recursive: true, force: true });
  }
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
