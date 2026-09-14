import { execFileSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { defaultResource, resourceFromAttributes, type Resource } from '@opentelemetry/resources';
import type { Config, Env } from './config';
import { DISTRO_NAME, VERSION } from './version';

// Resource detection, a port of the Go agent's resource.go: the same host.id chain as the infra agent
// (semantic-conventions.md §1), container.id from cgroup/mountinfo, OS, process and Kubernetes attributes.

/** Reads host files below a root prefix (tests use a temp dir; containers may mount the host root at /host). */
export class HostFS {
  constructor(readonly root = '/') {}

  path(p: string): string {
    const clean = path.posix.normalize('/' + p);
    if (!this.root || this.root === '/') return clean;
    return path.join(this.root, clean);
  }

  read(p: string): string | undefined {
    try {
      return fs.readFileSync(this.path(p), 'utf8');
    } catch {
      return undefined;
    }
  }

  readTrim(p: string): string {
    return (this.read(p) ?? '').trim();
  }
}

/** The infra agent's validity rule (agents/infra/internal/resource). */
export const VALID_HOST_ID = /^[0-9A-Za-z-]{8,}$/;

/** The infra agent's resolution chain (semantic-conventions.md §1). Keep in sync with agents/go/resource.go. */
export const HOST_ID_FILES = ['/etc/machine-id', '/var/lib/dbus/machine-id', '/sys/class/dmi/id/product_uuid'];

export type HostIdSource = 'config' | 'infra-agent' | 'infra-agent-state' | 'platform' | 'generated' | string;

/** Platform machine id on non-Linux systems (macOS IOPlatformUUID, Windows MachineGuid). Replaceable in tests. */
export const platform = {
  hostId(): string {
    try {
      if (process.platform === 'darwin') {
        const out = execFileSync('ioreg', ['-rd1', '-c', 'IOPlatformExpertDevice'], { encoding: 'utf8', timeout: 2000, stdio: ['ignore', 'pipe', 'ignore'] });
        const m = /"IOPlatformUUID"\s*=\s*"([^"]+)"/.exec(out);
        return m ? m[1].toLowerCase() : '';
      }
      if (process.platform === 'win32') {
        const out = execFileSync('reg', ['query', 'HKEY_LOCAL_MACHINE\\SOFTWARE\\Microsoft\\Cryptography', '/v', 'MachineGuid'], {
          encoding: 'utf8',
          timeout: 2000,
          stdio: ['ignore', 'pipe', 'ignore'],
        });
        const m = /MachineGuid\s+REG_SZ\s+(\S+)/.exec(out);
        return m ? m[1].toLowerCase() : '';
      }
    } catch {
      // not available
    }
    return '';
  },
};

function userCacheDir(): string {
  const home = os.homedir();
  if (process.platform === 'darwin') return path.join(home, 'Library', 'Caches');
  if (process.platform === 'win32') return process.env.LOCALAPPDATA || path.join(home, 'AppData', 'Local');
  return process.env.XDG_CACHE_HOME || path.join(home, '.cache');
}

/**
 * Resolves host.id like the infra agent, so both report the same id on one machine:
 *  0. the id a running infra agent published in <infraRuntimeDir>/host-id (under the host root, else the plain path)
 *  1. /etc/machine-id → /var/lib/dbus/machine-id → /sys/class/dmi/id/product_uuid (valid, not all zeros; lower-cased)
 *  2. the UUID the infra agent generated in its state dir (<infraStateDir>/host-id)
 *  3. non-Linux: the platform machine id
 *  4. a UUID generated and persisted by this agent (stateDir, default <user cache dir>/openlog/host-id)
 */
export function resolveHostId(hfs: HostFS, infraRuntimeDir: string, infraStateDir: string, stateDir: string, linux = process.platform === 'linux'): [string, HostIdSource] {
  if (infraRuntimeDir) {
    const file = path.posix.join(infraRuntimeDir, 'host-id');
    let v = hfs.readTrim(file);
    if (!VALID_HOST_ID.test(v) && hfs.path(file) !== path.posix.normalize(file)) v = new HostFS('/').readTrim(file);
    if (VALID_HOST_ID.test(v)) return [v, 'infra-agent'];
  }
  for (const p of HOST_ID_FILES) {
    const v = hfs.readTrim(p);
    if (VALID_HOST_ID.test(v) && v.replace(/[0-]/g, '') !== '') return [v.toLowerCase(), p];
  }
  if (infraStateDir) {
    const v = hfs.readTrim(path.posix.join(infraStateDir, 'host-id'));
    if (VALID_HOST_ID.test(v)) return [v, 'infra-agent-state'];
  }
  if (!linux) {
    const v = platform.hostId();
    if (v) return [v, 'platform'];
  }
  const dir = stateDir || path.join(userCacheDir(), 'openlog');
  const file = path.join(dir, 'host-id');
  try {
    const v = fs.readFileSync(file, 'utf8').trim();
    if (VALID_HOST_ID.test(v)) return [v, 'generated'];
  } catch {
    // generate below
  }
  const gen = randomUUID();
  try {
    fs.mkdirSync(dir, { recursive: true, mode: 0o750 });
    const tmp = `${file}.${process.pid}.tmp`;
    fs.writeFileSync(tmp, gen + '\n', { mode: 0o640 });
    fs.renameSync(tmp, file);
  } catch {
    return ['', ''];
  }
  return [gen, 'generated'];
}

const CONTAINER_ID_IN_CGROUP = /([0-9a-f]{64})(?:\.scope)?$/;
const CONTAINER_ID_IN_MOUNTINFO = /containers\/([0-9a-f]{64})\//;

/**
 * The id of the container this process runs in: the last 64-hex segment of /proc/self/cgroup (cgroup v1, or v2
 * without a cgroup namespace), otherwise the Docker/Podman container directory in /proc/self/mountinfo (cgroup v2 with
 * a private cgroup namespace, where /proc/self/cgroup is just "0::/").
 */
export function containerId(hfs: HostFS): string {
  const cgroup = hfs.read('/proc/self/cgroup');
  if (cgroup) {
    for (const line of cgroup.split('\n')) {
      const parts = line.split(':');
      if (parts.length < 3) continue;
      const segs = parts.slice(2).join(':').split('/');
      for (let i = segs.length - 1; i >= 0; i--) {
        const m = CONTAINER_ID_IN_CGROUP.exec(segs[i]);
        if (m) return m[1];
      }
    }
  }
  const mountinfo = hfs.read('/proc/self/mountinfo');
  if (mountinfo) {
    for (const line of mountinfo.split('\n')) {
      if (line.includes('/sandboxes/')) continue; // containerd pod sandbox, not this container
      const m = CONTAINER_ID_IN_MOUNTINFO.exec(line);
      if (m) return m[1];
    }
  }
  return '';
}

/** Maps kernel/Node architecture names to OTel host.arch values (same as the infra and Go agents). */
export function normalizeArch(a: string): string {
  switch (a) {
    case 'x86_64':
    case 'amd64':
    case 'x64':
      return 'amd64';
    case 'aarch64':
    case 'arm64':
      return 'arm64';
    case 'i386':
    case 'i686':
    case '386':
    case 'ia32':
      return 'x86';
    case 'armv7l':
    case 'armv6l':
    case 'arm':
      return 'arm32';
    case 'ppc64le':
    case 'ppc64':
      return 'ppc64';
    case 's390x':
      return 's390x';
  }
  return a;
}

export function osType(p: string = process.platform): string {
  switch (p) {
    case 'win32':
      return 'windows';
    case 'sunos':
      return 'solaris';
  }
  return p;
}

export function parseOSRelease(s: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const raw of s.split('\n')) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    const i = line.indexOf('=');
    if (i < 0) continue;
    let v = line.slice(i + 1);
    if (v.length >= 2 && ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'")))) {
      v = v.slice(1, -1).replace(/\\(["'\\$`])/g, '$1');
    }
    out[line.slice(0, i)] = v;
  }
  return out;
}

/** Kubernetes metadata from downward-API environment variables (inside a pod with fallbacks). */
export function k8sAttributes(hfs: HostFS, env: Env): Record<string, string> {
  const out: Record<string, string> = {};
  const e = (...names: string[]): string => {
    for (const n of names) {
      const v = env[n]?.trim();
      if (v) return v;
    }
    return '';
  };
  const set = (k: string, v: string): void => {
    if (v) out[k] = v;
  };
  set('k8s.pod.name', e('K8S_POD_NAME', 'POD_NAME'));
  set('k8s.pod.uid', e('K8S_POD_UID', 'POD_UID'));
  set('k8s.namespace.name', e('K8S_NAMESPACE_NAME', 'K8S_NAMESPACE', 'POD_NAMESPACE'));
  set('k8s.node.name', e('K8S_NODE_NAME', 'NODE_NAME'));
  set('k8s.container.name', e('K8S_CONTAINER_NAME', 'CONTAINER_NAME'));
  set('k8s.deployment.name', e('K8S_DEPLOYMENT_NAME'));
  set('k8s.cluster.name', e('K8S_CLUSTER_NAME'));
  if (env.KUBERNETES_SERVICE_HOST !== undefined) {
    if (!out['k8s.namespace.name']) set('k8s.namespace.name', hfs.readTrim('/var/run/secrets/kubernetes.io/serviceaccount/namespace'));
    if (!out['k8s.pod.name']) set('k8s.pod.name', e('HOSTNAME'));
  }
  return out;
}

function hostName(hfs: HostFS, linux: boolean): string {
  if (linux) {
    const v = hfs.readTrim('/proc/sys/kernel/hostname') || hfs.readTrim('/etc/hostname');
    if (v) return v;
  }
  return os.hostname();
}

export interface DetectOptions {
  linux?: boolean;
}

/** Host, OS, process, container and Kubernetes attributes, plus the host.id source. */
export function detectAttributes(cfg: Config, env: Env, opts: DetectOptions = {}): [Record<string, string | number>, HostIdSource] {
  const linux = opts.linux ?? process.platform === 'linux';
  const hfs = new HostFS(cfg.hostRoot);
  const a: Record<string, string | number> = {
    'host.name': hostName(hfs, linux),
    'host.arch': normalizeArch(process.arch),
    'os.type': osType(linux ? 'linux' : process.platform),
    'process.pid': process.pid,
    'process.executable.name': path.basename(process.execPath),
    'process.executable.path': process.execPath,
    'process.runtime.name': 'nodejs',
    'process.runtime.version': process.versions.node,
    'process.runtime.description': 'Node.js',
  };
  // The script path only: command-line arguments are not sent, they may contain secrets.
  if (process.argv[1]) a['process.command'] = process.argv[1];
  if (linux) {
    const arch = hfs.readTrim('/proc/sys/kernel/arch');
    if (arch) a['host.arch'] = normalizeArch(arch);
    for (const p of ['/etc/os-release', '/usr/lib/os-release']) {
      const s = hfs.read(p);
      if (s !== undefined) {
        const osr = parseOSRelease(s);
        if (osr.ID) a['os.name'] = osr.ID;
        if (osr.VERSION_ID) a['os.version'] = osr.VERSION_ID;
        if (osr.PRETTY_NAME) a['os.description'] = osr.PRETTY_NAME;
        break;
      }
    }
    const kr = hfs.readTrim('/proc/sys/kernel/osrelease');
    if (kr) a['openlog.os.kernel_release'] = kr;
    const cid = containerId(hfs);
    if (cid) a['container.id'] = cid;
  } else {
    a['os.version'] = os.release();
  }
  try {
    a['process.owner'] = os.userInfo().username;
  } catch {
    // no passwd entry (arbitrary container uid)
  }
  Object.assign(a, k8sAttributes(hfs, env));
  let source: HostIdSource = 'config';
  if (!cfg.hostId) {
    const [id, src] = resolveHostId(hfs, cfg.infraRuntimeDir, cfg.infraStateDir, cfg.stateDir, linux);
    source = src;
    if (id) a['host.id'] = id;
  }
  return [a, source];
}

/** Detected attributes < user resource attributes < explicit service/environment/host id settings. */
export function buildResource(cfg: Config, env: Env = process.env, opts: DetectOptions = {}): { resource: Resource; hostIdSource: HostIdSource } {
  const [attrs, hostIdSource] = detectAttributes(cfg, env, opts);
  Object.assign(attrs, cfg.resourceAttributes);
  attrs['service.name'] = cfg.serviceName;
  if (cfg.serviceVersion) attrs['service.version'] = cfg.serviceVersion;
  if (cfg.serviceNamespace) attrs['service.namespace'] = cfg.serviceNamespace;
  if (cfg.environment) attrs['deployment.environment.name'] = cfg.environment;
  if (cfg.hostId) attrs['host.id'] = cfg.hostId;
  attrs['telemetry.distro.name'] = DISTRO_NAME;
  attrs['telemetry.distro.version'] = VERSION;
  if (typeof attrs['process.pid'] === 'string') {
    const pid = Number(attrs['process.pid']);
    if (Number.isInteger(pid)) attrs['process.pid'] = pid;
  }
  // defaultResource() contributes telemetry.sdk.{name,language,version}; service.name is overridden.
  return { resource: defaultResource().merge(resourceFromAttributes(attrs)), hostIdSource };
}
