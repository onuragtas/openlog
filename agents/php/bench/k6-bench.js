// Closed-loop load on the Laravel /bench endpoint (one Eloquent query + two Redis calls).
// Env: TARGET (http://nginx-<variant>), VUS, DURATION, OUT (summary JSON path or empty).
import http from 'k6/http';
import { check } from 'k6';

export const options = {
  vus: Number(__ENV.VUS || 16),
  duration: __ENV.DURATION || '30s',
  discardResponseBodies: true,
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

export default function () {
  const id = Math.floor(Math.random() * 1000);
  const res = http.get(`${__ENV.TARGET}/bench/${id}`, { timeout: '10s' });
  check(res, { 'status 200': (r) => r.status === 200 });
}

export function handleSummary(data) {
  const out = {};
  if (__ENV.OUT) out[__ENV.OUT] = JSON.stringify(data);
  const d = data.metrics.http_req_duration.values;
  out.stdout = `reqs=${data.metrics.http_reqs.values.count} rps=${data.metrics.http_reqs.values.rate.toFixed(1)} ` +
    `p50=${d.med.toFixed(2)}ms p95=${d['p(95)'].toFixed(2)}ms failed=${(data.metrics.http_req_failed.values.rate * 100).toFixed(2)}%\n`;
  return out;
}
