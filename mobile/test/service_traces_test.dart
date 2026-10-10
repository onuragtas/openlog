// A service's traces: the filters that go out, and the ones that do not.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/services.dart';

import 'fake_server.dart';

Map<String, Object?> trace(String id, {bool error = false}) => {
  'trace_id': id,
  'span_id': 's$id',
  'timestamp': '2026-10-10T01:00:00.000000000Z',
  'duration_ms': 142.5,
  'is_error': error,
  'http_status_code': error ? 500 : 200,
  'service_name': 'checkout',
  'transaction_name': 'POST /checkout',
  'service_namespace': '',
  'environment': 'prod',
  'transaction_type': 'http',
};

void main() {
  test('an unchecked errors box is no filter, not error=false', () async {
    final queries = <String>[];
    final server = await FakeServer.start((req, seen) {
      queries.add(seen.query);
      writeJson(req, 200, {
        'traces': [trace('a')],
      });
    });
    addTearDown(server.stop);
    final c = ServiceTracesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      'checkout',
    );

    await c.refresh();
    c.errorsOnly = true;
    await c.refresh();

    // `error=false` asks for the requests that did not fail, which is not
    // what an unchecked box means.
    expect(queries.first, isNot(contains('error=')));
    expect(queries.first, contains('service=checkout'));
    expect(queries.last, contains('error=true'));
  });

  test(
    'empty filters are left out, filled ones go as the server names them',
    () async {
      final queries = <String>[];
      final server = await FakeServer.start((req, seen) {
        queries.add(Uri.decodeQueryComponent(seen.query));
        writeJson(req, 200, {'traces': <Object>[]});
      });
      addTearDown(server.stop);
      final c = ServiceTracesController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'checkout',
      );

      await c.refresh();
      expect(queries.single, isNot(contains('transaction=')));
      expect(queries.single, isNot(contains('min_duration_ms=')));

      c.transaction = 'POST /checkout';
      c.minDurationMs = '250';
      c.sort = 'duration';
      c.attributes = parseAttributeFilter('http.method=POST  user.tier=gold');
      await c.refresh();

      expect(queries.last, contains('transaction=POST /checkout'));
      expect(queries.last, contains('min_duration_ms=250'));
      expect(queries.last, contains('sort=duration'));
      expect(queries.last, contains('attr.http.method=POST'));
      expect(queries.last, contains('attr.user.tier=gold'));
    },
  );

  test('half-typed attribute filters are dropped, and ten is the limit', () {
    // `attr.=x` is a 400 and `attr.k=` filters on the empty string; neither
    // is what somebody halfway through typing meant.
    expect(parseAttributeFilter('=x k= ok=1'), {'ok': '1'});
    expect(parseAttributeFilter('a=1,b=2'), {'a': '1', 'b': '2'});

    final many = parseAttributeFilter(
      [for (var i = 0; i < 15; i++) 'k$i=$i'].join(' '),
    );
    expect(many, hasLength(10));
  });
}
