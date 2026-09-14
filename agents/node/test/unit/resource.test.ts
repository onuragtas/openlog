import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { after, test } from 'node:test';
import { loadConfig } from '../../src/config';
import { buildResource, containerId, HostFS, k8sAttributes, normalizeArch, parseOSRelease, platform, resolveHostId } from '../../src/resource';

const tmpRoots: string[] = [];
function root(files: Record<string, string>): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'openlog-node-res-'));
  tmpRoots.push(dir);
  for (const [p, content] of Object.entries(files)) {
    fs.mkdirSync(path.join(dir, path.dirname(p)), { recursive: true });
    fs.writeFileSync(path.join(dir, p), content);
  }
  return dir;
}
after(() => {
  for (const d of tmpRoots) fs.rmSync(d, { recursive: true, force: true });
});

const CID = 'a'.repeat(12) + '0123456789abcdef'.repeat(3) + 'b'.repeat(4);

test('host id chain (same as the infra and Go agents)', () => {
  const origPlatform = platform.hostId;
  platform.hostId = () => 'platform-uuid-1234';
  try {
    // 0. infra agent runtime file wins
    let r = root({ '/run/openlog-infra-agent/host-id': 'infra-published-id\n', '/etc/machine-id': 'ABCDEF0123456789\n' });
    assert.deepEqual(resolveHostId(new HostFS(r), '/run/openlog-infra-agent', '/var/lib/openlog-infra-agent', path.join(r, 'state'), true), ['infra-published-id', 'infra-agent']);
    // 1. machine-id lower-cased; all-zero ids skipped
    r = root({ '/etc/machine-id': '00000000000000000000\n', '/var/lib/dbus/machine-id': 'ABCDEF0123456789\n' });
    assert.deepEqual(resolveHostId(new HostFS(r), '/run/openlog-infra-agent', '', path.join(r, 'state'), true), ['abcdef0123456789', '/var/lib/dbus/machine-id']);
    // invalid (short) ids skipped → product_uuid
    r = root({ '/etc/machine-id': 'short', '/sys/class/dmi/id/product_uuid': 'EC2A1B2C-0000-1111-2222-333344445555' });
    assert.deepEqual(resolveHostId(new HostFS(r), '', '', path.join(r, 'state'), true), ['ec2a1b2c-0000-1111-2222-333344445555', '/sys/class/dmi/id/product_uuid']);
    // 2. infra agent state dir
    r = root({ '/var/lib/openlog-infra-agent/host-id': 'generated-by-infra-1\n' });
    assert.deepEqual(resolveHostId(new HostFS(r), '/run/openlog-infra-agent', '/var/lib/openlog-infra-agent', path.join(r, 'state'), true), ['generated-by-infra-1', 'infra-agent-state']);
    // 3. platform id only off Linux
    r = root({});
    assert.deepEqual(resolveHostId(new HostFS(r), '', '', path.join(r, 'state'), false), ['platform-uuid-1234', 'platform']);
    // 4. generated and persisted
    const [gen, src] = resolveHostId(new HostFS(r), '', '', path.join(r, 'state'), true);
    assert.equal(src, 'generated');
    assert.match(gen, /^[0-9a-f-]{36}$/);
    assert.deepEqual(resolveHostId(new HostFS(r), '', '', path.join(r, 'state'), true), [gen, 'generated']);
  } finally {
    platform.hostId = origPlatform;
  }
});

test('container id from cgroup v1/v2 and mountinfo', () => {
  assert.equal(containerId(new HostFS(root({ '/proc/self/cgroup': `12:pids:/docker/${CID}\n0::/\n` }))), CID);
  assert.equal(containerId(new HostFS(root({ '/proc/self/cgroup': `0::/system.slice/docker-${CID}.scope\n` }))), CID);
  assert.equal(containerId(new HostFS(root({ '/proc/self/cgroup': `0::/kubepods/besteffort/pod1/cri-containerd-${CID}.scope` }))), CID);
  const mountinfo = [
    `100 90 0:50 /var/lib/containerd/io.containerd.grpc.v1.cri/sandboxes/${'c'.repeat(64)}/hostname /etc/hostname rw - ext4 /dev/vda1 rw`,
    `101 90 254:1 /docker/containers/${CID}/resolv.conf /etc/resolv.conf rw,relatime - ext4 /dev/vda1 rw`,
  ].join('\n');
  assert.equal(containerId(new HostFS(root({ '/proc/self/cgroup': '0::/\n', '/proc/self/mountinfo': mountinfo }))), CID);
  assert.equal(containerId(new HostFS(root({ '/proc/self/cgroup': '0::/user.slice\n' }))), '');
});

test('helpers', () => {
  assert.equal(normalizeArch('x64'), 'amd64');
  assert.equal(normalizeArch('aarch64'), 'arm64');
  assert.equal(normalizeArch('ia32'), 'x86');
  assert.deepEqual(parseOSRelease('# c\nID=debian\nVERSION_ID="12"\nPRETTY_NAME="Debian GNU/Linux 12 (bookworm)"\n'), {
    ID: 'debian',
    VERSION_ID: '12',
    PRETTY_NAME: 'Debian GNU/Linux 12 (bookworm)',
  });
  const r = root({ '/var/run/secrets/kubernetes.io/serviceaccount/namespace': 'shop\n' });
  assert.deepEqual(k8sAttributes(new HostFS(r), { KUBERNETES_SERVICE_HOST: '10.0.0.1', HOSTNAME: 'web-7d9', K8S_NODE_NAME: 'node-1' }), {
    'k8s.namespace.name': 'shop',
    'k8s.pod.name': 'web-7d9',
    'k8s.node.name': 'node-1',
  });
});

test('buildResource precedence and attributes', () => {
  const r = root({
    '/etc/machine-id': 'feedfacefeedface\n',
    '/proc/sys/kernel/hostname': 'apm-host\n',
    '/proc/sys/kernel/arch': 'aarch64\n',
    '/proc/sys/kernel/osrelease': '6.8.0-test\n',
    '/etc/os-release': 'ID=alpine\nVERSION_ID=3.22.0\nPRETTY_NAME="Alpine Linux v3.22"\n',
    '/proc/self/cgroup': `0::/docker/${CID}\n`,
  });
  const env = {
    OPENLOG_HOST_ROOT: r,
    OPENLOG_INFRA_RUNTIME_DIR: '/nonexistent-openlog',
    OPENLOG_SERVICE_NAME: 'checkout',
    OPENLOG_SERVICE_VERSION: '2.0.0',
    OPENLOG_ENVIRONMENT: 'prod',
    OTEL_RESOURCE_ATTRIBUTES: 'service.name=ignored,team=payments,host.name=override',
  };
  const { config } = loadConfig(env);
  const { resource, hostIdSource } = buildResource(config, env, { linux: true });
  const a = resource.attributes;
  assert.equal(hostIdSource, '/etc/machine-id');
  assert.equal(a['service.name'], 'checkout');
  assert.equal(a['service.version'], '2.0.0');
  assert.equal(a['deployment.environment.name'], 'prod');
  assert.equal(a['host.id'], 'feedfacefeedface');
  assert.equal(a['host.name'], 'override');
  assert.equal(a['host.arch'], 'arm64');
  assert.equal(a['os.type'], 'linux');
  assert.equal(a['os.name'], 'alpine');
  assert.equal(a['openlog.os.kernel_release'], '6.8.0-test');
  assert.equal(a['container.id'], CID);
  assert.equal(a['team'], 'payments');
  assert.equal(a['process.pid'], process.pid);
  assert.equal(a['process.runtime.name'], 'nodejs');
  assert.equal(a['telemetry.distro.name'], 'openlog');
  assert.equal(a['telemetry.sdk.language'], 'nodejs');
  assert.equal(a['telemetry.sdk.name'], 'opentelemetry');

  const explicit = loadConfig({ ...env, OPENLOG_HOST_ID: 'explicit-host-1' }).config;
  const res2 = buildResource(explicit, env, { linux: true });
  assert.equal(res2.resource.attributes['host.id'], 'explicit-host-1');
  assert.equal(res2.hostIdSource, 'config');
});
