// The service list tabs ask the server to sort them, because "by time
// consumed" is a product of the calls and their durations over the whole
// range -- not something the rows on one page could be rearranged into.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/services.dart';

import 'fake_server.dart';

void main() {
  test('the sort goes to the server, which is the only place it can', () async {
    final queries = <String>[];
    final server = await FakeServer.start((req, seen) {
      queries.add(seen.query);
      if (seen.path.endsWith('/transactions')) {
        writeJson(req, 200, {'apdex_t_ms': 500.0, 'transactions': <Object>[]});
      } else {
        writeJson(req, 200, {'queries': <Object>[]});
      }
    });
    addTearDown(server.stop);
    final c = ServiceTransactionsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      'checkout',
    );
    final d = ServiceDatabasesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      'checkout',
    );

    await c.refresh();
    c.sort = 'slowest';
    await c.refresh();
    // The databases endpoint calls it `calls` where transactions say
    // `throughput`; the server names them, not this app.
    d.sort = 'calls';
    await d.refresh();

    expect(queries[0], contains('sort=time'));
    expect(queries[1], contains('sort=slowest'));
    expect(queries[2], contains('sort=calls'));
  });
}
