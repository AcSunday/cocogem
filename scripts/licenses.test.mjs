import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { licenseFiles } from './licenses.mjs';

test('collects real license files without including unrelated source', () => {
  const dir = mkdtempSync(join(tmpdir(), 'cocogem-license-'));
  try {
    for (const name of ['LICENSE', 'LICENSE-MIT', 'license.txt', 'COPYING', 'source.go']) writeFileSync(join(dir, name), 'fixture');
    assert.deepEqual(licenseFiles(dir), ['COPYING', 'LICENSE', 'LICENSE-MIT', 'license.txt']);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});
