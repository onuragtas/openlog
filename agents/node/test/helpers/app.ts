import { spawn, type ChildProcess } from 'node:child_process';
import * as path from 'node:path';

/** agents/node (compiled tests live in .test-build/test/…). */
export const PKG_ROOT = path.resolve(__dirname, '../../..');
export const REGISTER_CJS = path.join(PKG_ROOT, 'dist/cjs/register.js');
export const REGISTER_ESM = path.join(PKG_ROOT, 'dist/esm/register.js');
export const APPS = path.join(PKG_ROOT, 'test/apps');

export interface RunningApp {
  port: number;
  child: ChildProcess;
  stdout(): string;
  stderr(): string;
  get(p: string, headers?: Record<string, string>): Promise<{ status: number; body: string }>;
  /** Sends SIGTERM and resolves with the exit code/signal once the process is gone. */
  stop(signal?: NodeJS.Signals): Promise<{ code: number | null; signal: NodeJS.Signals | null }>;
}

export async function runApp(args: string[], env: Record<string, string>, nodeArgs: string[] = ['--require', REGISTER_CJS]): Promise<RunningApp> {
  const child = spawn(process.execPath, [...nodeArgs, ...args], {
    cwd: APPS,
    env: { PATH: process.env.PATH ?? '', HOME: process.env.HOME ?? '/tmp', OPENLOG_METRIC_EXPORT_INTERVAL: '500ms', ...env },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let out = '';
  let err = '';
  child.stdout!.on('data', (c: Buffer) => (out += c.toString()));
  child.stderr!.on('data', (c: Buffer) => (err += c.toString()));
  const exited = new Promise<{ code: number | null; signal: NodeJS.Signals | null }>((resolve) => child.on('exit', (code, signal) => resolve({ code, signal })));
  const port = await new Promise<number>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`app did not start: ${out}\n${err}`)), 30_000);
    const check = (): void => {
      const m = /READY (\d+)/.exec(out);
      if (m) {
        clearTimeout(timer);
        resolve(Number(m[1]));
      }
    };
    child.stdout!.on('data', check);
    void exited.then(() => {
      clearTimeout(timer);
      reject(new Error(`app exited early: ${out}\n${err}`));
    });
  });
  return {
    port,
    child,
    stdout: () => out,
    stderr: () => err,
    async get(p, headers = {}) {
      const res = await fetch(`http://127.0.0.1:${port}${p}`, { headers });
      return { status: res.status, body: await res.text() };
    },
    async stop(signal = 'SIGTERM') {
      if (child.exitCode === null && child.signalCode === null) child.kill(signal);
      const timer = setTimeout(() => child.kill('SIGKILL'), 15_000);
      const r = await exited;
      clearTimeout(timer);
      return r;
    },
  };
}
