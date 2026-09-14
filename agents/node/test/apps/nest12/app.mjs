// NestJS 12 (ESM-only) e2e app: `node --import <dist/esm/register.js> nest12/app.mjs [interceptor]`; prints "READY <port>".
// Dependencies: `npm ci --prefix test/apps/nest12`. Plain JavaScript, so decorators are applied as functions.
// Routes: GET /users/:id, GET /v2/orders/:orderId (URI versioning), GET /boom (500 with exception).
// With the `interceptor` argument the openlog Nest interceptor is registered globally (@openlog/node/nest).
import 'reflect-metadata';
import { Controller, Get, Module, Param, Version, VersioningType } from '@nestjs/common';
import { NestFactory } from '@nestjs/core';

function decorate(cls, method, ...decorators) {
  for (const d of decorators) {
    const desc = Object.getOwnPropertyDescriptor(cls.prototype, method);
    const out = d(cls.prototype, method, desc);
    if (out) Object.defineProperty(cls.prototype, method, out);
  }
}

class UsersController {
  get(id) {
    return { id };
  }
}
Controller('users')(UsersController);
decorate(UsersController, 'get', Get(':id'));
Param('id')(UsersController.prototype, 'get', 0);

class OrdersController {
  get(orderId) {
    return { orderId };
  }
}
Controller('orders')(OrdersController);
decorate(OrdersController, 'get', Get(':orderId'), Version('2'));
Param('orderId')(OrdersController.prototype, 'get', 0);

class BoomController {
  boom() {
    throw new TypeError('kaboom');
  }
}
Controller()(BoomController);
decorate(BoomController, 'boom', Get('boom'));

class AppModule {}
Module({ controllers: [UsersController, OrdersController, BoomController] })(AppModule);

let adapter;
if (process.argv.includes('fastify')) {
  const { FastifyAdapter } = await import('@nestjs/platform-fastify');
  adapter = new FastifyAdapter();
}
const app = adapter ? await NestFactory.create(AppModule, adapter, { logger: false }) : await NestFactory.create(AppModule, { logger: false });
app.enableVersioning({ type: VersioningType.URI });
if (process.argv.includes('interceptor')) {
  const { OpenLogNestInterceptor } = await import('../../../dist/esm/nest.js');
  app.useGlobalInterceptors(new OpenLogNestInterceptor());
}
await app.listen(0, '127.0.0.1');
console.log(`READY ${app.getHttpServer().address().port}`);
