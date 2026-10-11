// The browser application screen: the web's five tabs, and what each opens.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/detail.dart';
import 'package:openlog_mobile/src/rum.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/ui/rum_screen.dart';

import 'rum_test.dart' as data;

final _client = OpenlogClient(baseUrl: 'http://127.0.0.1:1');

class ScriptedOverview extends RumOverviewController {
  ScriptedOverview() : super(_client, 'shop-web') {
    value = RumOverview.fromJson({
      'from': '2026-10-11T08:00:00.000000000Z',
      'to': '2026-10-11T09:00:00.000000000Z',
      'step': '60s',
      'vitals': [
        data.vital('lcp'),
        data.vital('cls', unit: '', p75: 0.31, rating: 'poor'),
      ],
      'points': <Object>[],
      'totals': {'views': 1840, 'sessions': 612, 'errors': 16, 'avg_ms': 1280},
    });
    loaded = true;
  }
  @override
  Future<void> refresh() async {}
}

class ScriptedPages extends RumPagesController {
  ScriptedPages() : super(_client, 'shop-web') {
    items = [RumPage.fromJson(data.page('/checkout', views: 280))];
    loaded = true;
  }
  @override
  Future<void> refresh() async {}
}

class ScriptedRouteVitals extends RumRouteVitalsController {
  ScriptedRouteVitals(String route) : super(_client, 'shop-web', route) {
    value = RumVitalList.fromJson({
      'from': '2026-10-11T08:00:00.000000000Z',
      'to': '2026-10-11T09:00:00.000000000Z',
      'vitals': [data.vital('lcp', p75: 4300, rating: 'poor')],
    });
    loaded = true;
  }
  @override
  Future<void> refresh() async {}
}

class ScriptedSessions extends RumSessionsController {
  ScriptedSessions() : super(_client, 'shop-web') {
    items = [
      RumSession.fromJson(data.visit('9f2c41b7a80d4e6fb35c1d8e07a4b620')),
    ];
    loaded = true;
  }
  @override
  Future<void> refresh() async {}
}

/// The scripted sections of these screens, shared with the screenshot run.
Sections sectionsFor() => Sections(
  client: _client,
  rumOverview: (_) => ScriptedOverview(),
  rumPages: (_) => ScriptedPages(),
  rumSessions: (_) => ScriptedSessions(),
  rumRouteVitals: (_, route, _) => ScriptedRouteVitals(route),
);

Future<Sections> pump(WidgetTester tester) async {
  final sections = sectionsFor();
  addTearDown(sections.dispose);
  await tester.pumpWidget(
    MaterialApp(
      localizationsDelegates: L.localizationsDelegates,
      supportedLocales: L.supportedLocales,
      locale: const Locale('tr'),
      home: RumAppScreen(
        session: SessionController(store: MemoryTokenStore()),
        sections: sections,
        app: 'shop-web',
      ),
    ),
  );
  await tester.pumpAndSettle();
  return sections;
}

void main() {
  testWidgets('has the web five tabs, in the web order', (tester) async {
    await pump(tester);
    expect(
      tester.widgetList<Tab>(find.byType(Tab)).map((t) => t.text).toList(),
      ['Genel bakış', 'Sayfalar', 'Oturumlar', 'Sürümler', 'Huni'],
    );
  });

  testWidgets('the overview says what each vital is worth', (tester) async {
    await pump(tester);
    // The spread and the thresholds, not only the p75: one number is a
    // verdict, three are a shape.
    expect(find.textContaining('p50 1.62 s'), findsWidgets);
    expect(find.textContaining('İyi ≤ 2.5 s'), findsWidgets);
    expect(find.textContaining('Eşiğe 290 ms var'), findsOneWidget);
  });

  testWidgets('the pages tab lists the routes and opens one', (tester) async {
    await pump(tester);
    await tester.tap(find.byKey(const Key('rum-tab-pages')));
    await tester.pumpAndSettle();

    expect(find.text('/checkout'), findsOneWidget);
    await tester.tap(find.byKey(const Key('rum-page-/checkout')));
    await tester.pumpAndSettle();
    // The route's own screen, with the figures the row already had.
    expect(find.text('Sayfa'), findsOneWidget);
    expect(find.text('En yavaş'), findsOneWidget);
    // And the vitals of that route alone: poor here, good on the overview.
    expect(find.textContaining('4.3 s'), findsWidgets);
  });

  testWidgets('the sessions tab lists visits -- it showed none at all', (
    tester,
  ) async {
    await pump(tester);
    await tester.tap(find.byKey(const Key('rum-tab-sessions')));
    await tester.pumpAndSettle();

    expect(
      find.byKey(const Key('rum-session-9f2c41b7a80d4e6fb35c1d8e07a4b620')),
      findsOneWidget,
    );
    expect(find.textContaining('Chrome 131'), findsOneWidget);
  });

  testWidgets('the funnel asks for steps before it asks the server', (
    tester,
  ) async {
    await pump(tester);
    await tester.tap(find.byKey(const Key('rum-tab-funnel')));
    await tester.pumpAndSettle();

    expect(
      find.text('Huniyi görmek için en az iki adım yazın.'),
      findsOneWidget,
    );
  });
}
