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

  test('the web\'s sorts turn pages, and a text column starts at A', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.path.endsWith('/transactions')) {
        writeJson(req, 200, {
          'apdex_t_ms': 500.0,
          'transactions': <Object>[],
          'has_more': true,
        });
      } else if (seen.path.endsWith('/databases')) {
        writeJson(req, 200, {'queries': <Object>[]});
      } else {
        writeJson(req, 200, {'traces': <Object>[], 'has_more': true});
      }
    });
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x';
    Map<String, String> last() =>
        Uri.splitQueryString(server.requests.last.query);

    final t = ServiceTransactionsController(client, 'checkout');
    await t.refresh();
    expect(last()['dir'], 'desc');
    expect(last()['limit'], '50');
    await t.setSort('name');
    expect(last()['dir'], 'asc');
    await t.setSort('error_rate');
    expect(last()['dir'], 'desc');

    final d = ServiceDatabasesController(client, 'checkout');
    await d.setSort('database');
    expect(last()['sort'], 'database');
    expect(last()['dir'], 'asc');

    final traces = ServiceTracesController(client, 'checkout');
    await traces.refresh();
    expect(last()['sort'], 'timestamp');
    expect(last()['dir'], 'desc');
    await traces.setSort('timestamp');
    expect(last()['dir'], 'asc');
  });

  test(
    'the overview\'s top stays by time, however the list is sorted',
    () async {
      final server = await FakeServer.start((req, seen) {
        final sorted = Uri.splitQueryString(seen.query)['sort'];
        writeJson(req, 200, {
          'apdex_t_ms': 500.0,
          'transactions': [
            {
              'requests': 10.0,
              'throughput': 1.0,
              'errors': 0.0,
              'error_rate': 0.0,
              'transaction_type': 'request',
              'transaction_name': sorted == 'time' ? 'GET /heavy' : 'GET /a',
              'time_consumed_ms': 1.0,
              'time_share': 0.5,
              'max_ms': 1.0,
            },
          ],
        });
      });
      addTearDown(server.stop);
      final c = ServiceTransactionsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        'checkout',
      );

      await c.refresh();
      await c.setSort('name');

      expect(c.items.single.transactionName, 'GET /a');
      expect(c.topByTime.single.transactionName, 'GET /heavy');
    },
  );

  test('traces of a transaction are its slowest', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, {'traces': <Object>[]}),
    );
    addTearDown(server.stop);
    final c = ServiceTracesController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      'checkout',
    );

    await c.showTransaction('GET /orders');

    final q = Uri.splitQueryString(server.requests.single.query);
    expect(q['transaction'], 'GET /orders');
    expect(q['sort'], 'duration');
    expect(q['dir'], 'desc');
  });
}
