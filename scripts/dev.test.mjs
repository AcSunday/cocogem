import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createServer } from 'node:net';
import { backendEnv } from './dev-env.mjs';

async function available(port) {
  const server = createServer();
  return new Promise(resolve => {
    server.once('error', () => resolve(false));
    server.listen(port, '127.0.0.1', () => server.close(() => resolve(true)));
  });
}

test('development backend opts into the OS proxy unless configured', () => {
  assert.equal(backendEnv({}).COCOGEM_WEB_PROXY, 'system');
  assert.equal(backendEnv({ COCOGEM_WEB_PROXY: 'http://proxy.example:18080' }).COCOGEM_WEB_PROXY, 'http://proxy.example:18080');
  assert.equal(backendEnv({ COCOGEM_WEB_PROXY: '' }).COCOGEM_WEB_PROXY, '');
});

test('development supervisor starts both services and stops owned children', { timeout: 20000 }, async t => {
  if (!existsSync('dist/cocogem-server')) { t.skip('Run make build before this development smoke'); return; }
  if (!await available(8741) || !await available(5173)) { t.skip('Development ports are already in use'); return; }
  const dir = mkdtempSync(join(tmpdir(), 'cocogem-dev-smoke-'));
  const child = spawn(process.execPath, ['scripts/dev.mjs'], { env: { ...process.env, COCOGEM_DATA_DIR: dir }, stdio: ['ignore', 'pipe', 'pipe'] });
  const exited = new Promise(resolve => child.once('exit', resolve));
  let stderr = ''; child.stderr.on('data', data => { stderr += data; });
  child.stdout.resume();
  try {
    let ready = false;
    for (let i = 0; i < 150; i++) {
      try { const [api, ui] = await Promise.all([fetch('http://127.0.0.1:8741/api/v1/system'), fetch('http://127.0.0.1:5173/')]); if (api.status === 401 && ui.status === 200) { ready = true; break; } } catch { /* The direct children may still be starting. */ }
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    assert.equal(ready, true, stderr);
    child.kill('SIGTERM'); await exited;
    assert.equal(await available(8741), true, 'API child survived supervisor shutdown');
    assert.equal(await available(5173), true, 'UI child survived supervisor shutdown');
  } finally {
    if (child.exitCode === null && child.signalCode === null) { child.kill('SIGTERM'); await exited; }
    rmSync(dir, { recursive: true, force: true });
  }
});
