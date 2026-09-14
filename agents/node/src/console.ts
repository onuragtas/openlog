import { context } from '@opentelemetry/api';
import { SeverityNumber, type LoggerProvider } from '@opentelemetry/api-logs';
import { format } from 'node:util';
import { VERSION } from './version';

const METHODS = {
  debug: [SeverityNumber.DEBUG, 'DEBUG'],
  trace: [SeverityNumber.TRACE, 'TRACE'],
  log: [SeverityNumber.INFO, 'INFO'],
  info: [SeverityNumber.INFO, 'INFO'],
  warn: [SeverityNumber.WARN, 'WARN'],
  error: [SeverityNumber.ERROR, 'ERROR'],
} as const;

type Method = keyof typeof METHODS;

export interface ConsoleBridge {
  restore(): void;
}

/**
 * Opt-in (OPENLOG_LOGS_CONSOLE=true): every console.{log,info,warn,error,debug,trace} call is also exported as an
 * OTLP log record correlated with the active span; the original output is unchanged. Records emitted while exporting
 * (re-entrant calls) are skipped.
 */
export function bridgeConsole(provider: LoggerProvider): ConsoleBridge {
  const logger = provider.getLogger('@openlog/node/console', VERSION);
  const originals = new Map<Method, (...args: unknown[]) => void>();
  let inside = false;
  for (const m of Object.keys(METHODS) as Method[]) {
    const original = console[m] as (...args: unknown[]) => void;
    originals.set(m, original);
    const [severityNumber, severityText] = METHODS[m];
    const patched = function (this: unknown, ...args: unknown[]): void {
      original.apply(this ?? console, args);
      if (inside) return;
      inside = true;
      try {
        logger.emit({
          severityNumber,
          severityText,
          body: format(...args),
          context: context.active(),
          attributes: { 'log.source': 'console', 'code.function.name': `console.${m}` },
        });
      } catch {
        // never break the application's logging
      } finally {
        inside = false;
      }
    };
    (console as unknown as Record<string, unknown>)[m] = patched;
  }
  return {
    restore(): void {
      for (const [m, fn] of originals) (console as unknown as Record<string, unknown>)[m] = fn;
    },
  };
}
