// Build: CommonJS output with tsc (dist/cjs) plus a thin ES module facade (dist/esm) that re-exports the same
// instance, so `import` and `require` users share one agent (no dual-package state).
import { execFileSync } from 'node:child_process';
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const pkg = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));

// 1. src/version.ts follows package.json (the release job sets the version from the product tag).
const versionFile = join(root, 'src', 'version.ts');
const versionSrc = readFileSync(versionFile, 'utf8');
const nextSrc = versionSrc.replace(/export const VERSION = '[^']*';/, `export const VERSION = '${pkg.version}';`);
if (nextSrc !== versionSrc) writeFileSync(versionFile, nextSrc);

// 2. CommonJS build.
rmSync(join(root, 'dist'), { recursive: true, force: true });
execFileSync(process.execPath, [join(root, 'node_modules', 'typescript', 'bin', 'tsc'), '-p', join(root, 'tsconfig.json')], { stdio: 'inherit' });
writeFileSync(join(root, 'dist', 'cjs', 'package.json'), JSON.stringify({ type: 'commonjs' }) + '\n');

// 3. ES module facade.
const esm = join(root, 'dist', 'esm');
mkdirSync(esm, { recursive: true });
writeFileSync(join(esm, 'package.json'), JSON.stringify({ type: 'module' }) + '\n');
const require = createRequire(import.meta.url);
let names = [];
// index: the agent API; nest: the NestJS interceptor (openlog-node/nest).
for (const mod of ['index', 'nest']) {
  const exported = Object.keys(require(join(root, 'dist', 'cjs', `${mod}.js`))).filter((n) => /^[A-Za-z_$][\w$]*$/.test(n) && n !== 'default');
  if (mod === 'index') names = exported;
  writeFileSync(
    join(esm, `${mod}.js`),
    `// ES module facade over the CommonJS build (one shared agent instance).\n` +
      `import cjs from '../cjs/${mod}.js';\n\n` +
      `export const { ${exported.join(', ')} } = cjs;\n` +
      `export default cjs;\n`,
  );
  writeFileSync(join(esm, `${mod}.d.ts`), `export * from '../cjs/${mod}.js';\n`);
}
writeFileSync(
  join(esm, 'register.js'),
  `// node --import openlog-node/register app.mjs\n` +
    `// Registers the import-in-the-middle loader hook so ES module imports of instrumented libraries are patched, then\n` +
    `// starts the agent from the environment (same code path as --require).\n` +
    `import * as nodeModule from 'node:module';\n` +
    `\n` +
    `if (typeof nodeModule.register === 'function') {\n` +
    `  nodeModule.register('@opentelemetry/instrumentation/hook.mjs', import.meta.url);\n` +
    `}\n` +
    `const require = nodeModule.createRequire(import.meta.url);\n` +
    `require('../cjs/register.js');\n`,
);
console.log(`built openlog-node ${pkg.version}: dist/cjs (${names.length} exports), dist/esm facade`);
