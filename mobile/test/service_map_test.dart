// The service map as two lists: who calls this service, and what it calls.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/services.dart';

import 'fake_server.dart';

Map<String, Object?> node(String id, String name, String type) => {
  'id': id,
  'type': type,
  'name': name,
  'service_namespace': '',
  'environment': 'prod',
  'requests': 100.0,
  'throughput': 1.0,
  'error_rate': 0.0,
  'avg_ms': 10.0,
  'p95_ms': 20.0,
  'apdex': 0.98,
  'host_count': 1,
  'container_count': 0,
};

Map<String, Object?> edge(
  String id,
  String source,
  String target,
  String targetType,
  double calls,
) => {
  'id': id,
  'source': source,
  'target': target,
  'target_type': targetType,
  'calls': calls,
  'throughput': calls / 60,
  'errors': 0.0,
  'error_rate': 0.0,
  'avg_ms': 12.0,
  'p95_ms': 30.0,
};

void main() {
  test('edges are split by direction and the busiest comes first', () async {
    final server = await FakeServer.start((req, seen) {
      expect(seen.query, contains('service=checkout'));
      writeJson(req, 200, {
        'nodes': [
          node('n1', 'checkout', 'service'),
          node('n2', 'web', 'service'),
          node('n3', 'postgres', 'db'),
        ],
        'edges': [
          edge('e1', 'n2', 'n1', 'service', 40),
          edge('e2', 'n1', 'n3', 'db', 900),
        ],
      });
    });
    addTearDown(server.stop);
    final c = ServiceMapController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      'checkout',
    );

    await c.refresh();

    // Busiest first: the dependency carrying the most calls is the one an
    // incident is most likely about.
    expect(c.items.map((e) => e.id), ['e2', 'e1']);
    expect(c.selfId, 'n1');
    expect(c.incoming.map((e) => e.id), ['e1']);
    expect(c.outgoing.map((e) => e.id), ['e2']);
    // The name at the other end, not the id.
    expect(c.nodes[c.outgoing.single.target]?.name, 'postgres');
  });

  test('a transaction path marks edges and can be cleared', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.path.endsWith('/map/path')) {
        expect(seen.query, contains('transaction=POST'));
        writeJson(req, 200, {
          'trace_count': 12,
          'nodes': ['n1', 'n3'],
          'edges': ['e2'],
        });
        return;
      }
      writeJson(req, 200, {
        'nodes': [node('n1', 'checkout', 'service'), node('n3', 'db', 'db')],
        'edges': [edge('e2', 'n1', 'n3', 'db', 900)],
      });
    });
    addTearDown(server.stop);
    final c = ServiceMapController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      'checkout',
    );

    await c.refresh();
    await c.loadPath('POST /checkout');

    expect(c.pathEdges, {'e2'});
    expect(c.pathTraces, 12);

    c.clearPath();
    // Cleared rather than filtered away: an edge the transaction does not
    // use is still a dependency of the service.
    expect(c.pathEdges, isEmpty);
    expect(c.items, hasLength(1));
  });
}
