// Reports the gzipped size of the built SDK and fails when it grows past the budget.
//
// A RUM SDK is loaded by every visitor on every page, so its size is a feature, not a statistic: this check
// is what stops it drifting upwards one convenience at a time. Raise BUDGET deliberately, in a commit that
// says what was bought with it.
import { gzipSync } from 'node:zlib';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const BUDGET = 12 * 1024;

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const dist = join(root, 'dist');

let raw = 0;
const parts = [];
const walk = (d) => {
  for (const e of readdirSync(d)) {
    const p = join(d, e);
    if (statSync(p).isDirectory()) walk(p);
    else if (e.endsWith('.js')) {
      const src = readFileSync(p);
      raw += src.length;
      parts.push(src);
    }
  }
};
walk(dist);

// tsc does not minify, so this measures the shipped source gzipped; an application's bundler minifies it
// further. The budget is set against this number so the check needs no minifier dependency.
const gz = gzipSync(Buffer.concat(parts)).length;
console.log(`openlog-browser: ${raw} bytes raw, ${gz} bytes gzipped (budget ${BUDGET})`);
if (gz > BUDGET) {
  console.error(`size budget exceeded: ${gz} > ${BUDGET} bytes gzipped`);
  process.exit(1);
}
