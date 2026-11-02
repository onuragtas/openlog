// The analytics and crash tabs as somebody sees them on a phone.
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/ui/rum_analytics.dart';
import 'package:openlog_mobile/src/ui/rum_crashes.dart';

import 'analytics_test.dart' as data;

Future<void> pump(
  WidgetTester tester,
  Widget Function(SessionController, Sections) tab, {
  Object? Function(String path)? body,
  Set<String> missing = const {},
  String locale = 'en',
}) async {
  tester.view.physicalSize = const Size(390, 844);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  final client = OpenlogClient(
    baseUrl: 'http://example.test',
    httpClient: MockClient((req) async {
      final last = req.url.pathSegments.last;
      if (missing.contains(last)) {
        return http.Response(
          jsonEncode({
            'error': {'code': 'not_found', 'message': 'no such route'},
          }),
          404,
          headers: {'content-type': 'application/json'},
        );
      }
      return http.Response.bytes(
        utf8.encode(jsonEncode((body ?? data.bodyFor)(req.url.path))),
        200,
        headers: {'content-type': 'application/json'},
      );
    }),
  );
  final sections = Sections(client: client);
  addTearDown(sections.dispose);
  await tester.pumpWidget(
    MaterialApp(
      localizationsDelegates: L.localizationsDelegates,
      supportedLocales: L.supportedLocales,
      locale: Locale(locale),
      home: Scaffold(
        body: tab(SessionController(store: MemoryTokenStore()), sections),
      ),
    ),
  );
  await tester.pumpAndSettle();
}

Widget analytics(SessionController s, Sections x) =>
    RumAnalyticsTab(session: s, sections: x, app: 'shop-web');

Widget crashes(SessionController s, Sections x) =>
    RumCrashesTab(session: s, sections: x, app: 'shop-web');

Future<void> reveal(WidgetTester tester, Finder f) async {
  await tester.scrollUntilVisible(
    f,
    300,
    scrollable: find.byType(Scrollable).first,
  );
  await tester.pumpAndSettle();
}

void main() {
  group('analytics', () {
    testWidgets('the totals say how far each number moved', (tester) async {
      await pump(tester, analytics);

      final totals = find.byKey(const Key('analytics-totals'));
      expect(
        find.descendant(of: totals, matching: find.text('+200 (+20%)')),
        findsWidgets,
      );
      expect(
        find.descendant(of: totals, matching: find.text('−100 (−25%)')),
        findsOneWidget,
      );
      expect(
        find.descendant(of: totals, matching: find.text('+900 (new)')),
        findsOneWidget,
      );
      // Sessions have no knowable previous number.
      expect(
        find.descendant(of: totals, matching: find.text('–')),
        findsOneWidget,
      );
      expect(
        find.descendant(of: totals, matching: find.text('No change')),
        findsOneWidget,
      );
      expect(find.textContaining('2026-09-06'), findsOneWidget);
      expect(tester.takeException(), isNull);
    });

    testWidgets('the partial previous period is said', (tester) async {
      await pump(
        tester,
        analytics,
        body: (path) => path.endsWith('/compare')
            ? data.compareJson(partial: true)
            : data.bodyFor(path),
      );
      expect(
        find.byKey(const Key('analytics-previous-partial')),
        findsOneWidget,
      );
    });

    testWidgets('the lists are there, in the reader\'s language', (
      tester,
    ) async {
      await pump(tester, analytics, locale: 'tr');

      await reveal(tester, find.text('checkout_started'));
      expect(find.text('checkout_started'), findsOneWidget);
      await reveal(tester, find.text('(doğrudan)'));
      expect(find.text('(doğrudan)'), findsOneWidget);
      await reveal(tester, find.text('Türkiye'));
      expect(find.text('Türkiye'), findsOneWidget);
      expect(tester.takeException(), isNull);
    });

    testWidgets('retention writes every share and scrolls sideways', (
      tester,
    ) async {
      await pump(tester, analytics);

      final grid = find.byKey(const Key('analytics-retention'));
      await reveal(tester, grid);
      expect(
        find.descendant(of: grid, matching: find.text('100%')),
        findsNWidgets(2),
      );
      expect(
        find.descendant(of: grid, matching: find.text('40%')),
        findsOneWidget,
      );
      expect(
        find.descendant(of: grid, matching: find.text('Week 0')),
        findsOneWidget,
      );
      expect(
        tester.widget<SingleChildScrollView>(grid).scrollDirection,
        Axis.horizontal,
      );
    });

    testWidgets('nothing collected says what to turn on', (tester) async {
      await pump(
        tester,
        analytics,
        body: (path) => path.endsWith('/activity')
            ? data.activityJson(collecting: false)
            : data.bodyFor(path),
      );
      expect(find.byKey(const Key('analytics-not-collecting')), findsOneWidget);
      expect(find.textContaining('Browser keys'), findsOneWidget);
      expect(find.byKey(const Key('analytics-totals')), findsNothing);
    });

    testWidgets('no cohorts say the same, not an empty table', (tester) async {
      await pump(
        tester,
        analytics,
        body: (path) => path.endsWith('/retention')
            ? data.retentionJson(cohorts: 0)
            : data.bodyFor(path),
      );
      final state = find.byKey(const Key('analytics-not-collecting'));
      await reveal(tester, state);
      expect(state, findsOneWidget);
      expect(find.byKey(const Key('analytics-retention')), findsNothing);
    });

    testWidgets('a server older than all of it says so plainly', (
      tester,
    ) async {
      await pump(
        tester,
        analytics,
        missing: const {
          'activity',
          'compare',
          'retention',
          'events',
          'acquisition',
          'audience',
        },
      );
      expect(find.byKey(const Key('analytics-too-old')), findsOneWidget);
      expect(find.textContaining('too old'), findsOneWidget);
      // Not the failure wording.
      expect(find.textContaining('no such route'), findsNothing);
    });

    testWidgets('a server older than the comparison claims no zeros', (
      tester,
    ) async {
      await pump(tester, analytics, missing: const {'compare'});

      expect(
        find.byKey(const Key('analytics-compare-too-old')),
        findsOneWidget,
      );
      expect(find.textContaining('no such route'), findsNothing);
      final totals = find.byKey(const Key('analytics-totals'));
      // Users and the like are unknown; DAU still comes from the newest day.
      expect(
        find.descendant(of: totals, matching: find.text('0')),
        findsNothing,
      );
      expect(
        find.descendant(of: totals, matching: find.text('300')),
        findsOneWidget,
      );
    });
  });

  group('crashes', () {
    Map<String, Object?> crash({
      String frame = 'com.shop.Cart.checkout',
      List<String>? missing,
      String kind = 'uncaught_exception',
      String version = '1.4.2',
      String stack = 'at a.b.c(SourceFile:1)',
    }) => {
      'group_id': '00000000000000a1',
      'type': 'IllegalStateException',
      'message': 'cart is empty',
      'kind': kind,
      'count': 1234,
      'sessions': 9,
      'last_version': version,
      'last_seen': '2026-10-11T09:00:00.000000000Z',
      'stack': stack,
      'frame': frame,
      'missing_symbols': ?missing,
    };

    testWidgets('a crash shows its frame and what symbols it lacks', (
      tester,
    ) async {
      await pump(
        tester,
        crashes,
        body: (_) => {
          'crashes': [
            crash(missing: const ['libshop.so 1a2b', 'R8 mapping of 1.4.2']),
          ],
        },
      );
      expect(find.text('IllegalStateException'), findsOneWidget);
      expect(find.text('Uncaught exception'), findsOneWidget);
      expect(find.text('1,234'), findsOneWidget);
      expect(find.text('cart is empty'), findsOneWidget);
      expect(find.textContaining('9 sessions · last on 1.4.2'), findsOneWidget);
      expect(find.text('com.shop.Cart.checkout'), findsOneWidget);
      expect(
        find.text(
          'No symbols uploaded for: libshop.so 1a2b, R8 mapping of 1.4.2',
        ),
        findsOneWidget,
      );

      // The stack waits behind a tap.
      expect(find.text('at a.b.c(SourceFile:1)'), findsNothing);
      await tester.tap(find.text('Stack'));
      await tester.pumpAndSettle();
      expect(find.text('at a.b.c(SourceFile:1)'), findsOneWidget);
    });

    testWidgets('an older server\'s crash reads without the new fields', (
      tester,
    ) async {
      await pump(
        tester,
        crashes,
        body: (_) => {
          'crashes': [
            {
              'group_id': 'a1',
              'type': 'Boom',
              'message': '',
              'kind': 'something_new',
              'count': 1,
              'sessions': 1,
              'last_version': '',
              'last_seen': '2026-10-11T09:00:00.000000000Z',
              'stack': '',
            },
          ],
        },
      );
      expect(find.text('something_new'), findsOneWidget);
      expect(find.textContaining('(no version)'), findsOneWidget);
      expect(find.byKey(const Key('rum-crash-frame')), findsNothing);
      expect(find.byKey(const Key('rum-crash-missing-symbols')), findsNothing);
      expect(find.text('Stack'), findsNothing);
    });

    testWidgets('none is the web\'s sentence', (tester) async {
      await pump(tester, crashes, body: (_) => {'crashes': <Object>[]});
      expect(find.textContaining('Crash capture is off'), findsOneWidget);
    });

    testWidgets('a server without them is not told it has no crashes', (
      tester,
    ) async {
      await pump(tester, crashes, missing: const {'crashes'});
      expect(find.textContaining('too old'), findsOneWidget);
      expect(find.textContaining('No crashes'), findsNothing);
      expect(find.textContaining('no such route'), findsNothing);
    });
  });
}
