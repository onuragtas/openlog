// The containers list: its filters, and the grouped view.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/sections.dart';

import 'fake_server.dart';

ApiContainer container(
  String id, {
  String project = 'shop',
  String service = 'orders',
  String state = 'running',
  bool reporting = true,
  double? cpu = 0.2,
  double? memory = 1024,
}) => ApiContainer(
  containerId: id,
  name: id,
  imageName: 'ghcr.io/shop/orders:1',
  state: state,
  hostId: 'h1',
  hostName: 'web-1',
  composeProject: project,
  composeService: service,
  k8sPodName: '',
  k8sNamespaceName: '',
  k8sContainerName: '',
  cpuUtilization: cpu,
  memoryUsage: memory,
  memoryLimit: 4096,
  restartCount: 0,
  runtime: 'docker',
  health: '',
  imageTags: const [],
  cpuSparkline: const [],
  memorySparkline: const [],
  reporting: reporting,
  firstSeen: DateTime.utc(2026, 10, 1),
  lastSeen: DateTime.utc(2026, 10, 10),
);

void main() {
  group('grouping by compose service', () {
    test('one group per service, and the strays last', () {
      final groups = groupByComposeService([
        container('a'),
        container('b'),
        container('stray', project: '', service: ''),
        container('c', service: 'web'),
      ]);

      expect(
        [for (final g in groups) '${g.project}/${g.service}'],
        ['shop/orders', 'shop/web', '/'],
      );
      expect(groups.first.containers.length, 2);
      expect(groups.last.standalone, isTrue);
    });

    test('only what is reporting counts towards the numbers', () {
      final groups = groupByComposeService([
        container('a'),
        container('b', reporting: false, cpu: 9, memory: 9),
        container('c', state: 'exited'),
      ]);

      final g = groups.single;
      // Three containers, one running: a container that stopped reporting
      // is not running and its last numbers are not current.
      expect(g.containers.length, 3);
      expect(g.running, 1);
      expect(g.cpu, closeTo(0.4, 0.0001));
      expect(g.memory, 2048);
    });

    test('a group with no numbers at all keeps none', () {
      final groups = groupByComposeService([
        container('a', cpu: null, memory: null),
      ]);
      expect(groups.single.cpu, isNull);
      expect(groups.single.memory, isNull);
    });
  });

  test(
    'the filters go to the server, and "no project" is one of them',
    () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.path.endsWith('/groups')) {
          writeJson(req, 200, {
            'projects': [
              {
                'compose_project': 'shop',
                'host_ids': ['h1'],
                'containers': 3,
                'running': 3,
                'services': <Object>[],
              },
            ],
          });
        } else {
          writeJson(req, 200, {
            'containers': <Object>[],
            'total': 12,
            'step': '60s',
          });
        }
      });
      addTearDown(server.stop);
      final c =
          ContainersController(
              OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
            )
            ..hostId = 'h1'
            ..state = 'running'
            ..composeProject = '';

      await c.refresh();

      // `compose_project` with no value: the server asks whether the
      // parameter is there at all (`url.Values.Has`), which is how "the
      // containers in no project" is said on the wire.
      expect(
        Uri.decodeQueryComponent(server.requests.last.query),
        'host_id=h1&compose_project&state=running',
      );
      expect(c.projects.single.composeProject, 'shop');
      expect(c.total, 12);

      // The projects belong to the host they were asked for; the same host
      // is not asked twice.
      await c.refresh();
      expect(
        server.requests.where((r) => r.path.endsWith('/groups')).length,
        1,
      );
      c.hostId = 'h2';
      await c.refresh();
      expect(
        server.requests.where((r) => r.path.endsWith('/groups')).length,
        2,
      );
    },
  );
}
