import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { context, ROOT_CONTEXT, trace, type Span } from '@opentelemetry/api';
import { AsyncLocalStorageContextManager } from '@opentelemetry/context-async-hooks';
import { getRPCMetadata, RPCType, setRPCMetadata } from '@opentelemetry/core';
import { OpenLogNestInterceptor, type NestExecutionContextLike } from '../../src/nest';

before(() => {
  context.setGlobalContextManager(new AsyncLocalStorageContextManager().enable());
});
after(() => {
  context.disable();
});

function fakeSpan(recording = true): Span & { attrs: Record<string, unknown>; spanName: string } {
  const s = {
    attrs: {} as Record<string, unknown>,
    spanName: 'GET',
    isRecording: () => recording,
    setAttribute(k: string, v: unknown) {
      s.attrs[k] = v;
      return s;
    },
    updateName(n: string) {
      s.spanName = n;
      return s;
    },
  };
  return s as unknown as Span & { attrs: Record<string, unknown>; spanName: string };
}

function ctx(req: unknown, type = 'http'): NestExecutionContextLike {
  return {
    getType: () => type,
    getClass: () => class UsersController {},
    getHandler: () => function get() {},
    switchToHttp: () => ({ getRequest: () => req }),
  };
}

function inRequest<T>(span: Span, fn: () => T): { result: T; route: string | undefined } {
  const meta = { type: RPCType.HTTP as const, span };
  const c = setRPCMetadata(trace.setSpan(ROOT_CONTEXT, span), meta);
  const result = context.with(c, () => {
    const r = fn();
    assert.equal(getRPCMetadata(context.active()), meta);
    return r;
  });
  return { result, route: (meta as { route?: string }).route };
}

test('express request: route via RPC metadata, span renamed, controller/callback attributes', () => {
  const span = fakeSpan();
  const { result, route } = inRequest(span, () => new OpenLogNestInterceptor().intercept(ctx({ method: 'get', route: { path: '/v2/users/:id' } }), { handle: () => 'obs' }));
  assert.equal(result, 'obs');
  assert.equal(route, '/v2/users/:id');
  assert.equal(span.attrs['http.route'], '/v2/users/:id');
  assert.equal(span.spanName, 'GET /v2/users/:id');
  assert.equal(span.attrs['nestjs.controller'], 'UsersController');
  assert.equal(span.attrs['nestjs.callback'], 'get');
});

test('fastify request: routeOptions.url, then routerPath', () => {
  const a = fakeSpan();
  inRequest(a, () => new OpenLogNestInterceptor().intercept(ctx({ method: 'POST', routeOptions: { url: '/orders/:id' } }), { handle: () => 1 }));
  assert.equal(a.spanName, 'POST /orders/:id');
  const b = fakeSpan();
  inRequest(b, () => new OpenLogNestInterceptor().intercept(ctx({ method: 'GET', routerPath: '/legacy/:x' }), { handle: () => 1 }));
  assert.equal(b.attrs['http.route'], '/legacy/:x');
});

test('non-http contexts, missing routes, unrecording spans and throwing contexts are left alone', () => {
  const span = fakeSpan();
  const i = new OpenLogNestInterceptor();
  const { route } = inRequest(span, () => i.intercept(ctx({ method: 'GET', route: { path: '/x' } }, 'rpc'), { handle: () => 1 }));
  assert.equal(route, undefined);
  assert.deepEqual(span.attrs, {});

  const noRoute = fakeSpan();
  inRequest(noRoute, () => i.intercept(ctx({ method: 'GET', route: { path: /regex/ } }), { handle: () => 1 }));
  assert.equal(noRoute.attrs['http.route'], undefined);
  assert.equal(noRoute.spanName, 'GET');

  const off = fakeSpan(false);
  inRequest(off, () => i.intercept(ctx({ method: 'GET', route: { path: '/x' } }), { handle: () => 1 }));
  assert.deepEqual(off.attrs, {});

  const broken = { ...ctx({}), switchToHttp: () => { throw new Error('no http'); } };
  assert.equal(i.intercept(broken, { handle: () => 'ok' }), 'ok');
  // outside any request
  assert.equal(i.intercept(ctx({ method: 'GET', route: { path: '/x' } }), { handle: () => 'ok' }), 'ok');
});
