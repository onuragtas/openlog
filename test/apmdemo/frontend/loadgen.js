'use strict';

// Synthetic traffic for the frontend. Not instrumented (runs without --require).
const TARGET = (process.env.LOADGEN_TARGET || 'http://frontend:3000').replace(/\/$/, '');
const RPS = Math.max(0.1, Number(process.env.LOADGEN_RPS || 8));
const SUMMARY_MS = 30_000;

const rand = (min, max) => min + Math.floor(Math.random() * (max - min + 1));

const MIX = [
  { name: 'products', weight: 25, req: () => ({ path: '/api/products' }) },
  { name: 'product', weight: 25, req: () => ({ path: `/api/products/${rand(1, 20)}` }) },
  {
    name: 'create-order',
    weight: 15,
    req: () => ({
      path: '/api/orders',
      method: 'POST',
      body: { customer_id: rand(1, 50), product_id: rand(1, 20), quantity: rand(1, 5) },
    }),
  },
  { name: 'order', weight: 15, req: () => ({ path: `/api/orders/${rand(1, 120)}` }) },
  { name: 'recent', weight: 5, req: () => ({ path: '/api/orders/recent' }) },
  { name: 'report', weight: 3, req: () => ({ path: '/api/orders/report' }) },
  { name: 'checkout', weight: 7, req: () => ({ path: '/api/checkout' }) },
  { name: 'flaky', weight: 5, req: () => ({ path: '/api/flaky' }) },
];
const TOTAL_WEIGHT = MIX.reduce((s, m) => s + m.weight, 0);

function pick() {
  let r = Math.random() * TOTAL_WEIGHT;
  for (const m of MIX) {
    if ((r -= m.weight) < 0) return m;
  }
  return MIX[0];
}

let counts = {};
let inflight = 0;

async function fire() {
  const m = pick();
  const { path, method = 'GET', body } = m.req();
  inflight++;
  let key;
  try {
    const res = await fetch(TARGET + path, {
      method,
      headers: body ? { 'content-type': 'application/json' } : undefined,
      body: body ? JSON.stringify(body) : undefined,
      signal: AbortSignal.timeout(15_000),
    });
    await res.arrayBuffer();
    key = String(res.status);
  } catch (err) {
    key = `ERR:${err.cause?.code || err.name}`;
  } finally {
    inflight--;
  }
  counts[m.name] ??= {};
  counts[m.name][key] = (counts[m.name][key] || 0) + 1;
}

setInterval(() => {
  if (inflight > RPS * 20) return; // back off when the target is stuck
  fire();
}, 1000 / RPS);

setInterval(() => {
  const total = {};
  for (const byStatus of Object.values(counts)) {
    for (const [k, v] of Object.entries(byStatus)) total[k] = (total[k] || 0) + v;
  }
  console.log(`[loadgen] last ${SUMMARY_MS / 1000}s target=${TARGET} rps=${RPS} total=${JSON.stringify(total)} by_route=${JSON.stringify(counts)}`);
  counts = {};
}, SUMMARY_MS);

console.log(`[loadgen] started: target=${TARGET} rps=${RPS}`);
process.on('SIGTERM', () => process.exit(0));
process.on('SIGINT', () => process.exit(0));
