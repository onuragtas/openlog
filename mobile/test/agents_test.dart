// Agent versions: one row per service x agent x version, and what the
// filters keep.
//
// The flattening is the part worth testing. A service running two versions
// at once is a deploy halfway through, and collapsing the two would hide
// exactly the thing this screen exists for.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/agents.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';

import 'fake_server.dart';

Map<String, Object?> version(String v, String status, {int instances = 1}) => {
  'version': v,
  'status': status,
  'instances': instances,
  'spans': 1000,
  'last_seen': '2026-10-10T01:00:00.000000000Z',
};

Map<String, Object?> agent(
  String kind,
  List<Map<String, Object?>> versions, {
  String sdkName = '',
  String sdkLanguage = '',
}) => {
  'kind': kind,
  'distro_name': 'openlog',
  'sdk_name': sdkName,
  'sdk_language': sdkLanguage,
  'status': versions.first['status'],
  'instances': 1,
  'last_seen': '2026-10-10T01:00:00.000000000Z',
  'versions': versions,
  'versions_truncated': false,
  'instrumentation_modules': <String>[],
  'upgrade': null,
};

Map<String, Object?> service(
  String name,
  List<Map<String, Object?>> agents, {
  String environment = 'prod',
}) => {
  'service_name': name,
  'service_namespace': '',
  'environment': environment,
  'status': 'ok',
  'agents': agents,
};

Map<String, Object?> answer(List<Map<String, Object?>> services) => {
  'release': {
    'catalog': 'ok',
    'channel': 'stable',
    'latest': '0.2.0',
    'oldest_supported': '0.1.0',
    'notes_url': '',
  },
  'services': services,
};

Future<ApmAgentsController> loaded(List<Map<String, Object?>> services) async {
  final server = await FakeServer.start((req, seen) {
    // The upgrade commands make the server check package registries, and a
    // phone shows versions rather than running the upgrade.
    expect(seen.query, contains('upgrade=false'));
    writeJson(req, 200, answer(services));
  });
  addTearDown(server.stop);
  final c = ApmAgentsController(
    OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
  );
  await c.refresh();
  return c;
}

void main() {
  test('two versions of one agent are two rows', () async {
    final c = await loaded([
      service('checkout', [
        agent('go', [
          version('0.2.0', 'ok', instances: 3),
          version('0.1.9', 'outdated', instances: 1),
        ]),
      ]),
    ]);

    expect(c.items, hasLength(2));
    // Behind first: the rows worth acting on are why the screen is open.
    expect(c.items.first.version, '0.1.9');
    expect(c.items.first.status, ApmAgentStatus.outdated);
    expect(c.items.last.instances, 3);
    expect(c.release?.latest, '0.2.0');
  });

  test('openlog agents are named by their package, others by their SDK', () {
    final go = ApmServiceAgent.fromJson(agent('go', [version('1', 'ok')]));
    final other = ApmServiceAgent.fromJson(
      agent(
        'third_party',
        [version('1', 'third_party')],
        sdkName: 'opentelemetry',
        sdkLanguage: 'ruby',
      ),
    );

    expect(agentProduct(go), 'openlog-go');
    // A third-party distribution has no openlog package to name.
    expect(agentProduct(other), 'opentelemetry · ruby');
  });

  test('the filters work on what is loaded, without asking again', () async {
    final c = await loaded([
      service('checkout', [
        agent('go', [version('0.2.0', 'ok')]),
      ]),
      service('cart', [
        agent('node', [version('0.1.0', 'unsupported')]),
      ], environment: 'staging'),
    ]);

    c.attentionOnly = true;
    c.refilter();
    expect(c.items.map((r) => r.serviceName), ['cart']);

    c.attentionOnly = false;
    c.query = 'staging node';
    c.refilter();
    // Every term has to match, not just one: two words are a narrowing.
    expect(c.items.map((r) => r.serviceName), ['cart']);

    c.query = 'checkout node';
    c.refilter();
    expect(c.items, isEmpty);
  });
}
