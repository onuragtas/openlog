// The Databases section and what it opens, drawn. A file of its own and a
// mock client, because the test binding answers every real request with 400.
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/ui/app_db_queries.dart';
import 'package:openlog_mobile/src/ui/database_screen.dart';
import 'package:openlog_mobile/src/ui/databases_section.dart';
import 'package:openlog_mobile/src/ui/transaction_screen.dart';

const notOfferedKey = Key('not-offered');

Map<String, Object?> statementJson() => {
  'services': ['checkout'],
  'db_system': 'postgresql',
  'db_name': 'shop',
  'db_operation': 'SELECT',
  'statement': 'SELECT * FROM orders WHERE id = ?',
  'calls': 1200.0,
  'throughput': 20.0,
  'errors': 0.0,
  'error_rate': 0.0,
  'avg_ms': 12.5,
  'p95_ms': null,
  'max_ms': 90.0,
  'time_consumed_ms': 15000.0,
  'time_share': 0.25,
};

http.Response json(Object? body, [int status = 200]) => http.Response(
  jsonEncode(body),
  status,
  headers: {'content-type': 'application/json'},
);

http.Response noSuchEndpoint() => json({
  'error': {'code': 'not_found', 'message': 'no such endpoint'},
}, 404);

Sections sectionsAnswering(http.Response Function(Uri) answer) {
  final client = OpenlogClient(
    baseUrl: 'http://openlog.test',
    httpClient: MockClient((req) async => answer(req.url)),
  );
  return Sections(client: client);
}

Widget app(Widget home) => MaterialApp(
  localizationsDelegates: L.localizationsDelegates,
  supportedLocales: L.supportedLocales,
  locale: const Locale('en'),
  home: home,
);

SessionController session() => SessionController(store: MemoryTokenStore());

Widget section(Sections s) => app(
  Scaffold(
    body: DatabasesBody(session: session(), sections: s, active: true),
  ),
);

void main() {
  testWidgets('no server reports, so it opens on the applications', (
    tester,
  ) async {
    final s = sectionsAnswering(
      (url) => url.path == '/api/v1/db/instances'
          ? json({'instances': <Object>[]})
          : json({
              'queries': [statementJson()],
            }),
    );
    addTearDown(s.dispose);

    await tester.pumpWidget(section(s));
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('app-db-query-0')), findsOneWidget);
    expect(find.textContaining('SELECT * FROM orders'), findsOneWidget);
    // Missing p95 is a dash, never a zero.
    expect(find.text('–'), findsOneWidget);
  });

  testWidgets('a server reports, so it opens on the servers', (tester) async {
    final asked = <String>[];
    final s = sectionsAnswering((url) {
      asked.add(url.path);
      return url.path == '/api/v1/db/instances'
          ? json({
              'instances': [
                {
                  'instance': 'abc123',
                  'db_system': 'postgresql',
                  'host_id': 'h1',
                  'host_name': 'db1',
                  'server_address': 'db1.internal',
                  'server_port': 5432,
                  'calls': 10,
                  'throughput': 1.5,
                  'total_time_ms': 100.0,
                  'avg_ms': null,
                  'statements': 3,
                  'errors': 0,
                  'avg_active_sessions': null,
                  'top_wait': '',
                  'last_seen': '2026-10-10T09:00:00.000000000Z',
                },
              ],
            })
          : json({'queries': <Object>[]});
    });
    addTearDown(s.dispose);

    await tester.pumpWidget(section(s));
    await tester.pumpAndSettle();

    // Named after the server it monitors, not the id, which can be a hash.
    expect(find.text('db1.internal:5432'), findsOneWidget);
    // The other side is not asked until somebody looks at it.
    expect(asked, isNot(contains('/api/v1/apm/db-queries')));

    await tester.tap(find.text('From applications'));
    await tester.pumpAndSettle();

    expect(asked, contains('/api/v1/apm/db-queries'));
    expect(find.text('From servers'), findsOneWidget);
  });

  testWidgets('an older server: a sentence, not a failure banner', (
    tester,
  ) async {
    final s = sectionsAnswering(
      (url) => url.path == '/api/v1/db/instances'
          ? json({'instances': <Object>[]})
          : noSuchEndpoint(),
    );
    addTearDown(s.dispose);

    await tester.pumpWidget(section(s));
    await tester.pumpAndSettle();

    expect(s.appDbQueries.notOffered, isTrue);
    expect(s.appDbQueries.failure, isNull);
    final l = await L.delegate.load(const Locale('en'));
    expect(find.text(l.serverNotOffered), findsOneWidget);
    // Nothing to search or sort.
    expect(find.byKey(const Key('app-db-search')), findsNothing);
    expect(find.byKey(const Key('list-sort')), findsNothing);
  });

  group('one statement', () {
    final query = ApmDbQuery.fromJson(statementJson());

    Future<Sections> open(
      WidgetTester tester,
      http.Response Function(Uri) answer,
    ) async {
      final s = sectionsAnswering(answer);
      addTearDown(s.dispose);
      await tester.pumpWidget(
        app(AppDbQueryScreen(session: session(), sections: s, query: query)),
      );
      await tester.pumpAndSettle();
      return s;
    }

    testWidgets('its text, its plans and the generic note', (tester) async {
      await open(
        tester,
        (_) => json({
          'plans': [
            {
              'plan': 'Seq Scan on orders',
              'service': 'checkout',
              'generic': true,
              'captured_at': '2026-10-10T09:00:00.000000000Z',
              'trace_id': 'abc',
            },
          ],
        }),
      );

      expect(find.text(query.statement), findsOneWidget);
      expect(find.text('Seq Scan on orders'), findsOneWidget);
      expect(find.byKey(const Key('app-db-generic')), findsOneWidget);
      expect(find.byKey(const Key('app-db-service-checkout')), findsOneWidget);
      expect(find.byKey(const Key('app-db-no-plan')), findsNothing);
    });

    testWidgets('no plan says where plans come from', (tester) async {
      await open(tester, (_) => json({'plans': <Object>[]}));

      expect(find.byKey(const Key('app-db-no-plan')), findsOneWidget);
    });

    testWidgets('plans an older server has not got leave the rest', (
      tester,
    ) async {
      await open(tester, (_) => noSuchEndpoint());

      expect(find.byKey(notOfferedKey), findsOneWidget);
      expect(find.text(query.statement), findsOneWidget);
      expect(find.byKey(const Key('db-server-view')), findsOneWidget);
    });

    testWidgets('the server view asks only when tapped, and can say no', (
      tester,
    ) async {
      final asked = <String>[];
      await open(tester, (url) {
        asked.add(url.path);
        return url.path == '/api/v1/db/lookup'
            ? json({'matches': <Object>[]})
            : json({'plans': <Object>[]});
      });
      expect(asked, isNot(contains('/api/v1/db/lookup')));

      await tester.tap(find.byKey(const Key('db-server-view')));
      await tester.pumpAndSettle();

      expect(asked, contains('/api/v1/db/lookup'));
      expect(find.byKey(const Key('db-not-monitored')), findsOneWidget);
    });

    testWidgets('a match opens the statement on that instance', (tester) async {
      await open(tester, (url) {
        if (url.path == '/api/v1/db/lookup') {
          return json({
            'matches': [
              {
                'instance': 'db1:5432',
                'fingerprint': '42',
                'host_name': 'db1',
                'calls': 9,
                'avg_ms': 1.0,
              },
            ],
          });
        }
        if (url.path.startsWith('/api/v1/db/queries/')) return json({}, 500);
        return json({'plans': <Object>[]});
      });

      await tester.tap(find.byKey(const Key('db-server-view')));
      await tester.pumpAndSettle();

      expect(find.byType(DbQueryScreen), findsOneWidget);
      expect(find.text('db1:5432'), findsOneWidget);
    });

    testWidgets('an older server has no server view to offer', (tester) async {
      await open(
        tester,
        (url) => url.path == '/api/v1/db/lookup'
            ? noSuchEndpoint()
            : json({'plans': <Object>[]}),
      );

      await tester.tap(find.byKey(const Key('db-server-view')));
      await tester.pumpAndSettle();

      expect(find.byKey(notOfferedKey), findsOneWidget);
      expect(find.byKey(const Key('db-server-view')), findsNothing);
    });
  });

  group('database activity', () {
    Map<String, Object?> activity() => {
      'from': '2026-10-10T08:00:00.000000000Z',
      'to': '2026-10-10T09:00:00.000000000Z',
      'step': '60s',
      'series': <Object>[],
      'waits': [
        {'type': 'FromActivity', 'event': '', 'samples': 12, 'share': 0.4},
      ],
      'top_queries': <Object>[],
    };

    Widget screen(Sections s) => app(
      DatabaseScreen(
        session: session(),
        sections: s,
        instance: 'db1:5432',
        dbSystem: 'postgresql',
      ),
    );

    testWidgets('its lists are its own, and sort', (tester) async {
      final s = sectionsAnswering((url) {
        if (url.path == '/api/v1/db/activity') return json(activity());
        if (url.path.endsWith('/waits')) {
          return json({
            'waits': [
              {'type': 'FromList', 'event': '', 'samples': 2, 'share': 0.1},
            ],
          });
        }
        return json({
          'queries': [
            {
              'fingerprint': '7',
              'text': 'SELECT 7',
              'samples': 1,
              'avg_active_sessions': 0.5,
              'top_wait': 'CPU',
            },
          ],
        });
      });
      addTearDown(s.dispose);

      await tester.pumpWidget(screen(s));
      await tester.pumpAndSettle();

      expect(find.text('FromList'), findsOneWidget);
      expect(find.text('FromActivity'), findsNothing);
      expect(find.byKey(const Key('list-sort')), findsNWidgets(2));
      expect(find.byKey(const Key('db-top-7')), findsOneWidget);
    });

    testWidgets('an older server keeps the short lists, and says why', (
      tester,
    ) async {
      final s = sectionsAnswering(
        (url) => url.path == '/api/v1/db/activity'
            ? json(activity())
            : noSuchEndpoint(),
      );
      addTearDown(s.dispose);

      await tester.pumpWidget(screen(s));
      await tester.pumpAndSettle();

      expect(find.text('FromActivity'), findsOneWidget);
      expect(find.byKey(notOfferedKey), findsNWidgets(2));
      expect(find.byKey(const Key('list-sort')), findsNothing);
    });
  });

  group('one transaction', () {
    Widget screen(Sections s, {VoidCallback? onShowTraces}) => app(
      TransactionScreen(
        session: session(),
        sections: s,
        serviceName: 'checkout',
        name: 'GET /orders',
        onShowTraces: onShowTraces,
      ),
    );

    testWidgets('its numbers, its distribution and its slowest', (
      tester,
    ) async {
      Map<String, Object?> red() => {
        'requests': 100.0,
        'throughput': 2.5,
        'errors': 1.0,
        'error_rate': 0.01,
        'avg_ms': 12.0,
        'p50_ms': 10.0,
        'p95_ms': 40.0,
        'p99_ms': null,
        'apdex': null,
      };
      final s = sectionsAnswering(
        (_) => json({
          'transaction_name': 'GET /orders',
          'transaction_type': 'request',
          'step': '60s',
          'apdex_t_ms': 500.0,
          'totals': red(),
          'max_ms': 120.0,
          'series': [
            {...red(), 't': 1760000000000},
            {...red(), 't': 1760000060000},
          ],
          'histogram': [
            {'from_ms': 8.0, 'to_ms': 16.0, 'count': 5.0},
          ],
          'slowest': [
            {
              'trace_id': 'aaaabbbbccccddddeeee',
              'span_id': 'span1',
              'timestamp': '2026-10-10T09:00:00.000000000Z',
              'duration_ms': 120.0,
              'is_error': true,
              'http_status_code': 500,
              'service_name': 'checkout',
              'transaction_name': 'GET /orders',
            },
          ],
        }),
      );
      addTearDown(s.dispose);

      await tester.pumpWidget(screen(s));
      await tester.pumpAndSettle();

      expect(find.text('2.5 rpm'), findsOneWidget);
      expect(find.text('1%'), findsOneWidget);
      // No p99 and no Apdex are dashes, not zeros.
      expect(find.text('–'), findsNWidgets(2));
      expect(find.byKey(const Key('transaction-histogram')), findsOneWidget);
      await tester.scrollUntilVisible(
        find.byKey(const Key('transaction-sample-span1')),
        200,
        // The body's, and not the one inside the selectable name.
        scrollable: find.byType(Scrollable).first,
      );
      expect(find.text('500'), findsOneWidget);
    });

    testWidgets('an older server: a sentence, not a banner', (tester) async {
      final s = sectionsAnswering((_) => noSuchEndpoint());
      addTearDown(s.dispose);

      await tester.pumpWidget(screen(s));
      await tester.pumpAndSettle();

      expect(find.byKey(notOfferedKey), findsOneWidget);
      expect(find.byKey(const Key('transaction-totals')), findsNothing);
    });
  });
}
