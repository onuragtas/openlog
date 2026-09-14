// Overhead micro-benchmark: requests per second and latency of an Express app without the agent, with the agent
// (sampled, ratio 1) and with the agent at ratio 0 (spans not sampled), exporting to a local OTLP capture server.
//   npm run bench            (BENCH_SECONDS=10 BENCH_CONCURRENCY=16 to change the load)
import * as fs from 'node:fs';
import * as http from 'node:http';
import * as os from 'node:os';
import { runApp, REGISTER_CJS } from '../helpers/app';
import { startCapture } from '../helpers/otlp';

const SECONDS = Number(process.env.BENCH_SECONDS ?? 8);
const CONCURRENCY = Number(process.env.BENCH_CONCURRENCY ?? 16);
const ROUNDS = Number(process.env.BENCH_ROUNDS ?? 3);

async function load(port: number, seconds: number): Promise<{ rps: number; p50: number; p99: number }> {
  const agent = new http.Agent({ keepAlive: true, maxSockets: CONCURRENCY });
  const latencies: number[] = [];
  const end = Date.now() + seconds * 1000;
  const one = (): Promise<void> =>
    new Promise((resolve, reject) => {
      const t0 = process.hrtime.bigint();
      http
        .get({ host: '127.0.0.1', port, path: '/users/42', agent }, (res) => {
          res.resume();
          res.on('end', () => {
            latencies.push(Number(process.hrtime.bigint() - t0) / 1e6);
            resolve();
          });
        })
        .on('error', reject);
    });
  await Promise.all(
    Array.from({ length: CONCURRENCY }, async () => {
      while (Date.now() < end) await one();
    }),
  );
  agent.destroy();
  latencies.sort((a, b) => a - b);
  return { rps: latencies.length / seconds, p50: latencies[Math.floor(latencies.length * 0.5)], p99: latencies[Math.floor(latencies.length * 0.99)] };
}

async function measure(name: string, nodeArgs: string[], env: Record<string, string>): Promise<{ name: string; rps: number; p50: number; p99: number; rssMb: number }> {
  const results: { rps: number; p50: number; p99: number }[] = [];
  let rssMb = 0;
  for (let r = 0; r < ROUNDS; r++) {
    const app = await runApp(['web.cjs', 'express'], env, nodeArgs);
    await load(app.port, 2); // warm-up (JIT, connection pool)
    results.push(await load(app.port, SECONDS));
    const pid = app.child.pid!;
    try {
      const status = fs.readFileSync(`/proc/${pid}/status`, 'utf8');
      rssMb = Math.max(rssMb, Number(/VmRSS:\s+(\d+)/.exec(status)?.[1] ?? 0) / 1024);
    } catch {
      // not Linux
    }
    await app.stop();
  }
  const med = (k: 'rps' | 'p50' | 'p99'): number => results.map((x) => x[k]).sort((a, b) => a - b)[Math.floor(results.length / 2)];
  return { name, rps: med('rps'), p50: med('p50'), p99: med('p99'), rssMb };
}

async function main(): Promise<void> {
  const cap = await startCapture();
  const base = { OPENLOG_ENDPOINT: cap.url, OPENLOG_LICENSE_KEY: 'k', OPENLOG_SERVICE_NAME: 'bench', OPENLOG_METRIC_EXPORT_INTERVAL: '10s', NODE_ENV: 'production' };
  const rows = [
    await measure('no agent', [], base),
    await measure('agent, sampled (ratio 1)', ['--require', REGISTER_CJS], base),
    await measure('agent, not sampled (ratio 0)', ['--require', REGISTER_CJS], { ...base, OPENLOG_SAMPLING_RATIO: '0' }),
  ];
  await cap.close();
  const baseRps = rows[0].rps;
  console.log(`\nNode ${process.version}, ${os.cpus().length} CPUs, concurrency ${CONCURRENCY}, ${SECONDS}s x ${ROUNDS} rounds (median)\n`);
  console.log('| Scenario | req/s | vs. no agent | p50 ms | p99 ms | RSS MB |');
  console.log('|---|---|---|---|---|---|');
  for (const r of rows) {
    const delta = ((r.rps / baseRps - 1) * 100).toFixed(1);
    console.log(`| ${r.name} | ${r.rps.toFixed(0)} | ${r === rows[0] ? '—' : delta + ' %'} | ${r.p50.toFixed(2)} | ${r.p99.toFixed(2)} | ${r.rssMb ? r.rssMb.toFixed(0) : '—'} |`);
  }
  // The single-threaded server is saturated, so its service time per request is 1/rps.
  const perReq = (r: { rps: number }): string => (1000 / r.rps - 1000 / baseRps).toFixed(3);
  console.log(`\nadded service time per request (saturated single-threaded server): sampled ${perReq(rows[1])} ms, not sampled ${perReq(rows[2])} ms`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
