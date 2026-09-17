// Build: type-checked ES modules in dist/ (tsc only, no bundler). The package is ESM and side-effect free,
// so an application's own bundler tree-shakes the parts it does not use.
import { execFileSync } from 'node:child_process';
import { readFileSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const pkg = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));

// src/version.ts follows package.json (the release job sets the version from the product tag).
const versionFile = join(root, 'src', 'version.ts');
const current = readFileSync(versionFile, 'utf8');
const next = current.replace(/export const VERSION = '[^']*';/, `export const VERSION = '${pkg.version}';`);
if (next !== current) writeFileSync(versionFile, next);

rmSync(join(root, 'dist'), { recursive: true, force: true });
execFileSync(process.execPath, [join(root, 'node_modules', 'typescript', 'bin', 'tsc'), '-p', join(root, 'tsconfig.json')], {
  stdio: 'inherit',
});
console.log(`built openlog-browser ${pkg.version}: dist/ (ES modules)`);
