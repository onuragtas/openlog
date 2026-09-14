import type { ClientRequest, IncomingMessage, RequestOptions } from 'node:http';
import type { Instrumentation } from '@opentelemetry/instrumentation';
import { AwsInstrumentation } from '@opentelemetry/instrumentation-aws-sdk';
import { ExpressInstrumentation } from '@opentelemetry/instrumentation-express';
import { GraphQLInstrumentation } from '@opentelemetry/instrumentation-graphql';
import { GrpcInstrumentation } from '@opentelemetry/instrumentation-grpc';
import { HttpInstrumentation } from '@opentelemetry/instrumentation-http';
import { IORedisInstrumentation } from '@opentelemetry/instrumentation-ioredis';
import { KoaInstrumentation } from '@opentelemetry/instrumentation-koa';
import { MongoDBInstrumentation } from '@opentelemetry/instrumentation-mongodb';
import { MySQL2Instrumentation } from '@opentelemetry/instrumentation-mysql2';
import { NestInstrumentation } from '@opentelemetry/instrumentation-nestjs-core';
import { PgInstrumentation } from '@opentelemetry/instrumentation-pg';
import { PinoInstrumentation } from '@opentelemetry/instrumentation-pino';
import { RedisInstrumentation } from '@opentelemetry/instrumentation-redis';
import { UndiciInstrumentation } from '@opentelemetry/instrumentation-undici';
import { WinstonInstrumentation } from '@opentelemetry/instrumentation-winston';
import type { Config } from './config';
import { sanitizeKeyValue } from './sanitize';

// eslint-disable-next-line @typescript-eslint/no-require-imports
const FastifyOtelInstrumentation = require('@fastify/otel') as new (config?: Record<string, unknown>) => Instrumentation;

/** Short names accepted by OPENLOG_INSTRUMENTATIONS_DISABLED and `instrumentationConfig`, with their packages. */
export const INSTRUMENTATION_PACKAGES = {
  http: '@opentelemetry/instrumentation-http',
  undici: '@opentelemetry/instrumentation-undici',
  express: '@opentelemetry/instrumentation-express',
  fastify: '@fastify/otel',
  koa: '@opentelemetry/instrumentation-koa',
  nestjs: '@opentelemetry/instrumentation-nestjs-core',
  pg: '@opentelemetry/instrumentation-pg',
  mysql2: '@opentelemetry/instrumentation-mysql2',
  redis: '@opentelemetry/instrumentation-redis',
  ioredis: '@opentelemetry/instrumentation-ioredis',
  mongodb: '@opentelemetry/instrumentation-mongodb',
  graphql: '@opentelemetry/instrumentation-graphql',
  grpc: '@opentelemetry/instrumentation-grpc',
  'aws-sdk': '@opentelemetry/instrumentation-aws-sdk',
  pino: '@opentelemetry/instrumentation-pino',
  winston: '@opentelemetry/instrumentation-winston',
} as const;

export type InstrumentationName = keyof typeof INSTRUMENTATION_PACKAGES;

const ALIASES: Record<string, InstrumentationName> = {
  https: 'http',
  fetch: 'undici',
  'nestjs-core': 'nestjs',
  nest: 'nestjs',
  aws: 'aws-sdk',
  'redis-4': 'redis',
  mysql: 'mysql2',
};

/** Normalizes user-given names (short names, aliases, full package names, OTel auto-instrumentation names). */
export function normalizeInstrumentationName(name: string): InstrumentationName | undefined {
  let n = name.trim().toLowerCase();
  for (const [short, pkg] of Object.entries(INSTRUMENTATION_PACKAGES)) {
    if (n === pkg) return short as InstrumentationName;
  }
  n = n.replace(/^@opentelemetry\/instrumentation-/, '');
  if (n in INSTRUMENTATION_PACKAGES) return n as InstrumentationName;
  return ALIASES[n];
}

function pathOf(url: string | undefined): string {
  if (!url) return '/';
  const q = url.search(/[?#]/);
  return q < 0 ? url : url.slice(0, q);
}

export interface CreatedInstrumentations {
  instrumentations: Instrumentation[];
  unknownDisabled: string[];
}

/** Builds the enabled instrumentations with the agent defaults merged with `config.instrumentationConfig`. */
export function createInstrumentations(cfg: Config): CreatedInstrumentations {
  const disabled = new Set<InstrumentationName>();
  const unknownDisabled: string[] = [];
  for (const n of cfg.disabledInstrumentations) {
    const norm = normalizeInstrumentationName(n);
    if (norm) disabled.add(norm);
    else unknownDisabled.push(n);
  }
  const user = (name: InstrumentationName): Record<string, unknown> => {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(cfg.instrumentationConfig)) {
      if (normalizeInstrumentationName(k) === name) Object.assign(out, v);
    }
    return out;
  };

  const endpoint = new URL(cfg.endpoint);
  const endpointPort = endpoint.port || (endpoint.protocol === 'https:' ? '443' : '80');
  const basePath = endpoint.pathname.replace(/\/+$/, '');
  const isExport = (o: RequestOptions): boolean => {
    const host = (o.hostname ?? o.host ?? '').replace(/:\d+$/, '').replace(/^\[|\]$/g, '');
    const port = String(o.port ?? (o.protocol === 'https:' ? 443 : 80));
    return host === endpoint.hostname.replace(/^\[|\]$/g, '') && port === endpointPort && (o.path ?? '').startsWith(basePath + '/v1/');
  };
  const ignorePaths = new Set(cfg.httpIgnorePaths);
  const kvSerializer = (cmd: string, args: ReadonlyArray<unknown>): string =>
    cfg.dbQueryText === 'raw' ? [cmd, ...args.map((a) => (Buffer.isBuffer(a) ? a.toString() : String(a)))].join(' ') : cfg.dbQueryText === 'off' ? '' : sanitizeKeyValue(cmd, args);

  const factories: Record<InstrumentationName, (c: Record<string, unknown>) => Instrumentation> = {
    http: (c) =>
      new HttpInstrumentation({
        ignoreIncomingRequestHook: (req: IncomingMessage) => ignorePaths.size > 0 && ignorePaths.has(pathOf(req.url)),
        ignoreOutgoingRequestHook: (o: RequestOptions | ClientRequest) => isExport(o as RequestOptions),
        ...c,
      }),
    undici: (c) => new UndiciInstrumentation(c),
    express: (c) => new ExpressInstrumentation(c),
    fastify: (c) => new FastifyOtelInstrumentation({ registerOnInitialization: true, ...c }),
    koa: (c) => new KoaInstrumentation(c),
    nestjs: (c) => new NestInstrumentation(c),
    pg: (c) => new PgInstrumentation({ enhancedDatabaseReporting: false, ...c }),
    mysql2: (c) => new MySQL2Instrumentation({ maskStatement: cfg.dbQueryText !== 'raw', ...c }),
    redis: (c) => new RedisInstrumentation({ dbStatementSerializer: kvSerializer, ...c }),
    ioredis: (c) => new IORedisInstrumentation({ dbStatementSerializer: kvSerializer, ...c }),
    mongodb: (c) => new MongoDBInstrumentation({ enhancedDatabaseReporting: false, ...c }),
    graphql: (c) => new GraphQLInstrumentation({ ignoreTrivialResolveSpans: true, ...c }),
    grpc: (c) => new GrpcInstrumentation(c),
    'aws-sdk': (c) => new AwsInstrumentation(c),
    pino: (c) => new PinoInstrumentation({ disableLogSending: !cfg.logsExport, ...c }),
    winston: (c) => new WinstonInstrumentation({ disableLogSending: !cfg.logsExport, ...c }),
  };

  const instrumentations: Instrumentation[] = [];
  for (const name of Object.keys(factories) as InstrumentationName[]) {
    if (disabled.has(name)) continue;
    instrumentations.push(factories[name](user(name)));
  }
  return { instrumentations, unknownDisabled };
}
