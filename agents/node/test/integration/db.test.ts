// Instrumentation tests against real PostgreSQL, MySQL and Redis (test/integration/docker-compose.yml).
import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { runApp } from '../helpers/app';
import { startCapture, type Capture } from '../helpers/otlp';

const CLIENT = 3;
const skip = !process.env.PG_URL || !process.env.MYSQL_URL || !process.env.REDIS_URL ? 'PG_URL, MYSQL_URL and REDIS_URL not set' : false;

let cap: Capture;
before(async () => {
  cap = await startCapture();
});
after(async () => {
  await cap.close();
});

test('pg, mysql2, redis and ioredis client spans with sanitized statements', { skip }, async () => {
  const app = await runApp(['db.cjs'], {
    OPENLOG_ENDPOINT: cap.url,
    OPENLOG_LICENSE_KEY: 'k',
    OPENLOG_SERVICE_NAME: 'e2e-db',
    OPENLOG_RUNTIME_METRICS: 'false',
    PG_URL: process.env.PG_URL!,
    MYSQL_URL: process.env.MYSQL_URL!,
    REDIS_URL: process.env.REDIS_URL!,
  });
  try {
    assert.equal((await app.get('/db')).status, 200);
    const server = await cap.waitFor('GET /db', () => cap.spans.find((s) => s.kind === 2 && s.resource['service.name'] === 'e2e-db'));
    const db = await cap.waitFor('db client spans', () => {
      const spans = cap.spans.filter((s) => s.traceId === server.traceId && s.kind === CLIENT && (s.attributes['db.system.name'] ?? s.attributes['db.system']));
      return spans.length >= 8 ? spans : undefined;
    });
    const texts = db.map((s) => `${s.attributes['db.system.name'] ?? s.attributes['db.system']}: ${s.attributes['db.query.text'] ?? s.attributes['db.statement']}`);
    const joined = texts.join('\n');
    for (const secret of ['secret-note', 'my-secret', 'secret-value', 'secret-item', 'products:42']) {
      assert.ok(!joined.includes(secret), `statement leaks ${secret}:\n${joined}`);
    }
    const want = [
      'postgresql: INSERT INTO orders (id, note) VALUES (?) ON CONFLICT (id) DO UPDATE SET note = ?',
      'postgresql: SELECT * FROM orders WHERE id = $1 AND note <> $2',
      'mysql: INSERT INTO orders (id, note) VALUES (?) ON DUPLICATE KEY UPDATE note = ?',
      'mysql: SELECT * FROM orders WHERE id IN (?) AND note = ?',
      'redis: SET ? ?',
      'redis: GET ?',
      'redis: HSET ? ? ?',
    ];
    for (const w of want) assert.ok(texts.includes(w), `missing ${JSON.stringify(w)} in:\n${joined}`);
    const failed = db.find((s) => String(s.attributes['db.query.text'] ?? '').includes('missing_table'))!;
    assert.equal(failed.status.code, 2, 'failed query span has status ERROR');
    for (const s of db) {
      assert.equal(s.parentSpanId !== '', true);
      assert.ok(s.attributes['server.address'] || s.attributes['net.peer.name'], `server.address on ${s.name}`);
    }
  } finally {
    await app.stop();
  }
});
