// `@openlog/node/nest`: NestJS route naming that does not depend on a NestJS instrumentation (NestJS 12 is ESM-only and
// outside the range of @opentelemetry/instrumentation-nestjs-core). No dependency on @nestjs packages: the interceptor is
// typed structurally and Nest only needs an object with an `intercept(context, next)` method.
import { context, trace, type Span } from '@opentelemetry/api';
import { getRPCMetadata, RPCType } from '@opentelemetry/core';

/** The parts of Nest's ExecutionContext the interceptor reads. */
export interface NestExecutionContextLike {
  getType(): string;
  getClass(): { readonly name: string } | undefined;
  getHandler(): { readonly name: string } | undefined;
  switchToHttp(): { getRequest(): unknown };
}

/** The parts of Nest's CallHandler the interceptor uses. */
export interface NestCallHandlerLike<T = unknown> {
  handle(): T;
}

interface RequestLike {
  method?: string;
  /** express: the matched Route (`path` is the registered pattern, including global prefix and URI version). */
  route?: { path?: unknown };
  /** fastify 4.10+/5: the registered route URL. */
  routeOptions?: { url?: unknown };
  /** fastify < 4.10. */
  routerPath?: unknown;
  /** Nest on fastify passes the raw request in some adapters; express has none. */
  raw?: RequestLike;
}

function routeOf(req: RequestLike | undefined): string | undefined {
  if (!req || typeof req !== 'object') return undefined;
  for (const candidate of [req.route?.path, req.routeOptions?.url, req.routerPath]) {
    if (typeof candidate === 'string' && candidate !== '' && candidate !== '*') return candidate;
  }
  return undefined;
}

export const NEST_CONTROLLER_ATTRIBUTE = 'nestjs.controller';
export const NEST_CALLBACK_ATTRIBUTE = 'nestjs.callback';

/**
 * Names the entry HTTP SERVER span of a NestJS request after the matched route: sets `http.route` (through the HTTP
 * instrumentation's RPC metadata, as express/koa/fastify instrumentations do) and renames the span `<METHOD> <route>`,
 * plus `nestjs.controller` / `nestjs.callback`. Works for NestJS 8–12 on the express and fastify platforms.
 *
 *   import { OpenLogNestInterceptor } from '@openlog/node/nest';
 *   app.useGlobalInterceptors(new OpenLogNestInterceptor());
 *   // or: providers: [{ provide: APP_INTERCEPTOR, useClass: OpenLogNestInterceptor }]
 *
 * Interceptors run after middleware and guards, so requests rejected before the handler (guards, 404) keep the name
 * the platform instrumentation gave them. Never throws into the application.
 */
export class OpenLogNestInterceptor {
  intercept<T>(ctx: NestExecutionContextLike, next: NestCallHandlerLike<T>): T {
    try {
      annotate(ctx);
    } catch {
      // naming must never break a request
    }
    return next.handle();
  }
}

function annotate(ctx: NestExecutionContextLike): void {
  if (ctx.getType() !== 'http') return;
  const req = ctx.switchToHttp().getRequest() as RequestLike | undefined;
  const route = routeOf(req) ?? routeOf(req?.raw);
  const active = context.active();
  const rpc = getRPCMetadata(active);
  const span: Span | undefined = rpc?.type === RPCType.HTTP ? rpc.span : undefined;
  if (rpc?.type === RPCType.HTTP && route) rpc.route = route;
  const target = span ?? trace.getSpan(active);
  if (!target || !target.isRecording()) return;
  const controller = ctx.getClass()?.name;
  const callback = ctx.getHandler()?.name;
  if (controller) target.setAttribute(NEST_CONTROLLER_ATTRIBUTE, controller);
  if (callback) target.setAttribute(NEST_CALLBACK_ATTRIBUTE, callback);
  if (span && route) {
    span.setAttribute('http.route', route);
    const method = req?.method ?? req?.raw?.method;
    if (typeof method === 'string' && method) span.updateName(`${method.toUpperCase()} ${route}`);
  }
}
