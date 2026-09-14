'use strict';
// Integration app: one HTTP request runs queries against PostgreSQL (pg), MySQL (mysql2), Redis (redis) and Redis
// (ioredis) inside the request span. Prints "READY <port>".
const http = require('node:http');
const { Client } = require('pg');
const mysql = require('mysql2/promise');
const { createClient } = require('redis');
const IORedis = require('ioredis');

async function main() {
  const pg = new Client({ connectionString: process.env.PG_URL });
  await pg.connect();
  const my = await mysql.createConnection(process.env.MYSQL_URL);
  const redis = createClient({ url: process.env.REDIS_URL });
  await redis.connect();
  const ioredis = new IORedis(process.env.REDIS_URL);

  await pg.query('CREATE TABLE IF NOT EXISTS orders (id int primary key, note text)');
  await my.query('CREATE TABLE IF NOT EXISTS orders (id int primary key, note text)');

  const server = http.createServer(async (req, res) => {
    try {
      if (req.url === '/db') {
        await pg.query("INSERT INTO orders (id, note) VALUES (1, 'secret-note') ON CONFLICT (id) DO UPDATE SET note = 'secret-note'");
        await pg.query('SELECT * FROM orders WHERE id = $1 AND note <> $2', [1, 'x']);
        await my.query("INSERT INTO orders (id, note) VALUES (7, 'my-secret') ON DUPLICATE KEY UPDATE note = 'my-secret'");
        await my.query('SELECT * FROM orders WHERE id IN (1, 2, 3) AND note = ?', ['my-secret']);
        await redis.set('products:42', 'secret-value');
        await redis.get('products:42');
        await ioredis.hset('cart:9', 'item', 'secret-item');
        await pg.query('SELECT broken FROM missing_table').catch(() => {});
        res.end('ok');
      } else {
        res.statusCode = 404;
        res.end();
      }
    } catch (err) {
      res.statusCode = 500;
      res.end(String(err));
    }
  });
  server.listen(0, '127.0.0.1', () => console.log(`READY ${server.address().port}`));
  const close = async () => {
    server.close();
    await Promise.allSettled([pg.end(), my.end(), redis.quit(), ioredis.quit()]);
  };
  process.on('SIGTERM', () => {
    close().finally(() => setTimeout(() => process.exit(0), 3000));
  });
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
