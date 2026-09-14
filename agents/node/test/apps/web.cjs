'use strict';
// E2E app started with `node --require <dist/cjs/register.js> web.cjs <framework>`; prints "READY <port>".
// Frameworks: express, fastify, koa, nest, http. Each serves GET /users/:id, GET /boom (500 with exception),
// GET /call (outgoing fetch + http.get to /users/7 on itself) and GET /logs (pino, winston, console).
const http = require('node:http');

const framework = process.argv[2] || 'express';

const pino = require('pino')({ level: 'info' }, process.stdout);
const winston = require('winston');
const wlog = winston.createLogger({ transports: [new winston.transports.Console()] });

let port = 0;

async function callSelf() {
  const a = await fetch(`http://127.0.0.1:${port}/users/7`).then((r) => r.text());
  const b = await new Promise((resolve, reject) => {
    http
      .get(`http://127.0.0.1:${port}/users/8`, (res) => {
        let body = '';
        res.on('data', (c) => (body += c));
        res.on('end', () => resolve(body));
      })
      .on('error', reject);
  });
  return { a, b };
}

function logs() {
  pino.info({ order_id: 42 }, 'pino order placed');
  wlog.info('winston order placed', { order_id: 43 });
  console.log('console order placed');
}

async function main() {
  let server;
  if (framework === 'express') {
    const express = require('express');
    const app = express();
    const router = express.Router();
    router.get('/:id', (req, res) => res.json({ id: req.params.id }));
    app.use('/users', router);
    app.get('/boom', () => {
      throw new TypeError('kaboom');
    });
    app.get('/call', async (req, res) => res.json(await callSelf()));
    app.get('/logs', (req, res) => {
      logs();
      res.json({ ok: true });
    });
    server = http.createServer(app);
  } else if (framework === 'fastify') {
    const fastify = require('fastify')();
    fastify.get('/users/:id', async (req) => ({ id: req.params.id }));
    fastify.get('/boom', async () => {
      throw new TypeError('kaboom');
    });
    fastify.get('/call', async () => callSelf());
    fastify.get('/logs', async () => {
      logs();
      return { ok: true };
    });
    await fastify.listen({ port: 0, host: '127.0.0.1' });
    port = fastify.server.address().port;
    console.log(`READY ${port}`);
    return;
  } else if (framework === 'koa') {
    const Koa = require('koa');
    const Router = require('@koa/router');
    const app = new Koa();
    const router = new Router();
    router.get('/users/:id', (ctx) => {
      ctx.body = { id: ctx.params.id };
    });
    router.get('/boom', () => {
      throw new TypeError('kaboom');
    });
    router.get('/call', async (ctx) => {
      ctx.body = await callSelf();
    });
    router.get('/logs', (ctx) => {
      logs();
      ctx.body = { ok: true };
    });
    app.use(router.routes());
    server = http.createServer(app.callback());
  } else if (framework === 'nest') {
    require('reflect-metadata');
    const { NestFactory } = require('@nestjs/core');
    const { Controller, Get, Module, Param } = require('@nestjs/common');
    class UsersController {
      get(id) {
        return { id };
      }
    }
    Controller('users')(UsersController);
    const d = Object.getOwnPropertyDescriptor(UsersController.prototype, 'get');
    Get(':id')(UsersController.prototype, 'get', d);
    Param('id')(UsersController.prototype, 'get', 0);
    class AppModule {}
    Module({ controllers: [UsersController] })(AppModule);
    const app = await NestFactory.create(AppModule, { logger: false });
    await app.listen(0, '127.0.0.1');
    port = app.getHttpServer().address().port;
    console.log(`READY ${port}`);
    return;
  } else {
    server = http.createServer(async (req, res) => {
      if (req.url.startsWith('/users/')) {
        res.setHeader('content-type', 'application/json');
        res.end(JSON.stringify({ id: req.url.slice(7) }));
      } else if (req.url === '/call') {
        res.end(JSON.stringify(await callSelf()));
      } else if (req.url === '/logs') {
        logs();
        res.end('{}');
      } else {
        res.statusCode = 404;
        res.end();
      }
    });
  }
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  port = server.address().port;
  console.log(`READY ${port}`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
