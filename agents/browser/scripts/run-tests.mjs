// Runs the compiled node:test suites (tsc -p tsconfig.test.json writes them to .test-build).
// Mirrors agents/node/scripts/run-tests.mjs.
import { spawnSync } from 'node:child_process';
import { readdirSync, statSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const files = [];
const walk = (d) => {
  let entries;
  try {
    entries = readdirSync(d);
  } catch {
    return;
  }
  for (const e of entries) {
    const p = join(d, e);
    if (statSync(p).isDirectory()) walk(p);
    else if (e.endsWith('.test.js')) files.push(p);
  }
};
walk(join(root, '.test-build', 'test'));
files.sort();
if (files.length === 0) {
  console.error('no compiled tests found; run `tsc -p tsconfig.test.json` first');
  process.exit(1);
}
const res = spawnSync(process.execPath, ['--test', '--test-reporter=spec', ...files], { stdio: 'inherit', cwd: root });
process.exit(res.status ?? 1);
