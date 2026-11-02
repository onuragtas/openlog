// One transaction, the shape of its latency, and how the numbers read.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/apm_format.dart';
import 'package:openlog_mobile/src/logs.dart';
import 'package:openlog_mobile/src/services.dart';

import 'fake_server.dart';

Map<String, Object?> red() => {
  'requests': 100.0,
  'throughput': 2.5,
  'errors': 1.0,
  'error_rate': 0.01,
  'avg_ms': 12.0,
  'p50_ms': 10.0,
  'p95_ms': 40.0,
  'p99_ms': 90.0,
  'apdex': 0.97,
};

Map<String, Object?> detail() => {
  'transaction_name': 'GET /orders',
  'transaction_type': 'request',
  'step': '60s',
  'apdex_t_ms': 500.0,
  'totals': red(),
  'max_ms': 120.0,
  'series': [
    {...red(), 't': 1760000000000},
  ],
  'histogram': [
    {'from_ms': 8.0, 'to_ms': 16.0, 'count': 5.0},
  ],
  'slowest': [
    {
      'trace_id': 'aaaa',
      'span_id': 'bbbb',
      'timestamp': '2026-10-10T09:00:00.000000000Z',
      'duration_ms': 120.0,
      'is_error': true,
      'http_status_code': 500,
      'service_name': 'checkout',
      'transaction_name': 'GET /orders',
    },
  ],
};

void main() {
  test('asks for the one transaction, by name', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, detail()),
    );
    addTearDown(server.stop);
    final c = ApmTransactionController(
      OpenlogClient(baseUrl: server.baseUrl),
      'checkout',
      'GET /orders',
    );

    await c.refresh();

    expect(
      server.requests.single.path,
      '/api/v1/apm/services/checkout/transaction',
    );
    expect(
      Uri.splitQueryString(server.requests.single.query)['name'],
      'GET /orders',
    );
    expect(c.value!.totals.p95Ms, 40);
    expect(c.value!.slowest.single.isError, isTrue);
  });

  test('an older server\'s 404 is a state, not "it is gone"', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 404, {
        'error': {'code': 'not_found', 'message': 'no such endpoint'},
      }),
    );
    addTearDown(server.stop);
    final c = ApmTransactionController(
      OpenlogClient(baseUrl: server.baseUrl),
      'checkout',
      'GET /orders',
    );

    await c.refresh();

    expect(c.notOffered, isTrue);
    expect(c.failure, isNull);
  });

  test(
    'a required field missing is said, not thrown past the screen',
    () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, {'transaction_name': 'GET /orders'}),
      );
      addTearDown(server.stop);
      final c = ApmTransactionController(
        OpenlogClient(baseUrl: server.baseUrl),
        'checkout',
        'GET /orders',
      );

      await c.refresh();

      expect(c.failure?.kind, 'unexpected');
      expect(c.value, isNull);
    },
  );

  test('its logs are asked for by transaction and service together', () async {
    final server = await FakeServer.start(
      (req, _) => writeJson(req, 200, {'rows': <Object>[], 'has_more': false}),
    );
    addTearDown(server.stop);
    final c = LogsController(
      OpenlogClient(baseUrl: server.baseUrl),
      transaction: 'GET /orders',
      transactionService: 'checkout',
    );

    await c.refresh();

    expect(c.scoped, isTrue);
    final body = server.requests.single.body;
    expect(body, contains('"transaction":"GET /orders"'));
    expect(body, contains('"transaction_service":"checkout"'));
    // A transaction's logs are all of them, like a request's.
    expect(body, isNot(contains('severity_number')));
  });

  group('the latency distribution', () {
    ApmHistogramBin bin(double to, double count) =>
        ApmHistogramBin(fromMs: 0, toMs: to, count: count);

    test(
      'nothing has no bars',
      () => expect(shapeHistogram(const []), isEmpty),
    );

    test('the empty buckets between two filled ones are drawn', () {
      // Buckets 8 and 11, at eight to the octave: 2 ms and about 2.6 ms.
      final bars = shapeHistogram([bin(2, 3), bin(2.59, 1)]);
      expect(bars, hasLength(4));
      expect(bars.map((b) => b.count), [3, 0, 0, 1]);
      expect(bars.first.toMs, closeTo(2, 1e-9));
    });

    test('at most the web\'s 32 bars, and every request still counted', () {
      final bars = shapeHistogram([bin(1, 5), bin(1048576, 7)]);
      expect(bars.length, lessThanOrEqualTo(32));
      expect(bars.map((b) => b.count).reduce((a, b) => a + b), 12);
    });
  });

  group('how a number reads, as on the web', () {
    test('a duration', () {
      expect(formatApmMs(null), '–');
      expect(formatApmMs(0.5), '0.5 ms');
      expect(formatApmMs(5.25), '5.3 ms');
      expect(formatApmMs(250), '250 ms');
      expect(formatApmMs(1500), '1.5 s');
      expect(formatApmMs(90000), '1.5 min');
    });

    test('a rate: zero is zero, a small one keeps its digits', () {
      expect(formatApmRate(null), '–');
      expect(formatApmRate(0), '0%');
      expect(formatApmRate(0.0012), '0.12%');
      expect(formatApmRate(0.25), '25%');
      expect(formatApmRate(0.125), '12.5%');
    });

    test('a throughput and an Apdex', () {
      expect(formatApmRpm(2.5), '2.5 rpm');
      expect(formatApmRpm(120), '120 rpm');
      expect(formatApmRpm(null), '–');
      expect(formatApmApdex(0.9), '0.90');
      expect(formatApmApdex(null), '–');
    });
  });
}
