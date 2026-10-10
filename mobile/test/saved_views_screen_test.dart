// The views button and its sheet, on the logs explorer.
//
// Scripted controller rather than a server, like the other widget tests: what
// matters here is that applying a view changes the list and says what it
// could not show, and that saving sends what is actually on screen.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/logs.dart';
import 'package:openlog_mobile/src/saved_views.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/ui/logs_screen.dart';
import 'package:openlog_mobile/src/ui/traces_screen.dart';

final _client = OpenlogClient(baseUrl: 'http://127.0.0.1:1');

SavedView savedView(
  String id, {
  String name = 'Ödeme hataları',
  String visibility = 'private',
  bool canEdit = true,
  Map<String, Object?> state = const {},
}) => SavedView(
  id: id,
  signal: SavedViewSignal.logs,
  name: name,
  description: '',
  visibility: visibility == 'org'
      ? SavedViewVisibility.org
      : SavedViewVisibility.private,
  state: state,
  createdByUserId: 'u1',
  createdByEmail: 'owner@example.com',
  canEdit: canEdit,
  createdAt: DateTime.utc(2026, 10, 1, 9),
  updatedAt: DateTime.utc(2026, 10, 1, 9),
);

class ScriptedViews extends SavedViewsController {
  ScriptedViews({List<SavedView> views = const [], bool unavailable = false})
    : super(_client, signal: 'logs') {
    this.views = views;
    this.unavailable = unavailable;
    loaded = true;
  }

  final created = <Map<String, Object?>>[];
  final overwritten = <String>[];
  final removed = <String>[];

  @override
  Future<void> load() async {}

  @override
  Future<SavedView?> create({
    required String name,
    required String visibility,
    required Map<String, Object?> state,
  }) async {
    created.add({'name': name, 'visibility': visibility, 'state': state});
    final view = savedView('new', name: name, state: state);
    views = [...views, view];
    activeId = view.id;
    notifyListeners();
    return view;
  }

  @override
  Future<SavedView?> overwrite(
    SavedView view,
    Map<String, Object?> state,
  ) async {
    overwritten.add('${view.id}:${state['q']}');
    return view;
  }

  @override
  Future<bool> remove(String id) async {
    removed.add(id);
    views = [
      for (final v in views)
        if (v.id != id) v,
    ];
    notifyListeners();
    return true;
  }
}

class ScriptedTraces extends TracesController {
  ScriptedTraces() : super(_client) {
    items = const [];
    loaded = true;
  }

  int refreshes = 0;

  // Notifies, like the real one: the screen's search box follows the
  // controller, and a fake that stays quiet would hide that.
  @override
  Future<void> refresh() async {
    refreshes++;
    notifyListeners();
  }
}

class ScriptedLogs extends LogsController {
  ScriptedLogs() : super(_client) {
    items = const [];
    loaded = true;
  }

  int refreshes = 0;

  @override
  Future<void> refresh() async {
    refreshes++;
    notifyListeners();
  }
}

Future<ScriptedLogs> pump(WidgetTester tester, ScriptedViews views) async {
  final logs = ScriptedLogs();
  // The holder disposes everything it was given, the views controller
  // included, so the test must not dispose it a second time.
  final sections = Sections(client: _client, logViews: views);
  addTearDown(sections.dispose);
  addTearDown(logs.dispose);
  await tester.pumpWidget(
    MaterialApp(
      localizationsDelegates: L.localizationsDelegates,
      supportedLocales: L.supportedLocales,
      locale: const Locale('tr'),
      home: Scaffold(
        body: LogsBody(
          session: SessionController(store: MemoryTokenStore()),
          sections: sections,
          logs: logs,
          patterns: sections.logPatterns,
          volume: sections.logVolume,
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return logs;
}

void main() {
  testWidgets('applying a view sets the filters, the search and the severity', (
    tester,
  ) async {
    final views = ScriptedViews(
      views: [
        savedView(
          'v1',
          state: const {
            'filters': [
              {'key': 'service.name', 'op': '=', 'value': 'checkout'},
              {'key': 'severity_number', 'op': '>=', 'value': 17},
            ],
            'q': 'timeout',
          },
        ),
      ],
    );
    final logs = await pump(tester, views);

    await tester.tap(find.byKey(const Key('saved-views')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('view-v1')));
    await tester.pumpAndSettle();

    expect(logs.query, 'timeout');
    expect(logs.severityMin, 'ERROR');
    expect([for (final f in logs.filters) f.key], ['service.name']);
    expect(logs.refreshes, greaterThan(0));
    // The box has to show what the list is filtered by, not the word that
    // was in it before the view was applied.
    expect(
      tester
          .widget<TextField>(find.byKey(const Key('logs-search')))
          .controller
          ?.text,
      'timeout',
    );
    // And the button now says which view is on screen.
    expect(find.text('Ödeme hataları'), findsOneWidget);
  });

  testWidgets('applying a view that carries nothing says so', (tester) async {
    final views = ScriptedViews(
      views: [
        savedView('v1', state: const {'filters': [], 'q': ''}),
      ],
    );
    await pump(tester, views);

    await tester.tap(find.byKey(const Key('saved-views')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('view-v1')));
    await tester.pumpAndSettle();

    // The list cannot change, so the screen says why rather than
    // looking like the tap was missed.
    expect(
      find.text('Bu görünümde bu ekranın uygulayabileceği bir koşul yok.'),
      findsOneWidget,
    );
  });

  testWidgets('applying a view says which one', (tester) async {
    final views = ScriptedViews(
      views: [
        savedView(
          'v1',
          state: const {
            'filters': [
              {'key': 'service.name', 'op': '=', 'value': 'checkout'},
            ],
          },
        ),
      ],
    );
    await pump(tester, views);

    await tester.tap(find.byKey(const Key('saved-views')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('view-v1')));
    await tester.pumpAndSettle();

    expect(find.text('Ödeme hataları uygulandı'), findsOneWidget);
  });

  testWidgets('a view with OR groups says what it could not show', (
    tester,
  ) async {
    final views = ScriptedViews(
      views: [
        savedView(
          'v1',
          state: const {
            'filters': [
              {'key': 'service.name', 'op': '=', 'value': 'checkout'},
            ],
            'groups': [
              [
                {'key': 'severity_text', 'op': '=', 'value': 'ERROR'},
              ],
            ],
          },
        ),
      ],
    );
    await pump(tester, views);

    await tester.tap(find.byKey(const Key('saved-views')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('view-v1')));
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('view-applied')), findsOneWidget);
  });

  testWidgets('saving sends what is on screen, under the chosen visibility', (
    tester,
  ) async {
    final views = ScriptedViews();
    final logs = await pump(tester, views);
    logs.query = 'timeout';
    logs.severityMin = 'WARN';

    await tester.tap(find.byKey(const Key('saved-views')));
    await tester.pumpAndSettle();
    expect(find.byKey(const Key('views-empty')), findsOneWidget);
    // Nothing to save until it has a name: a button that takes the tap and
    // does nothing is indistinguishable from one that failed.
    expect(
      tester.widget<FilledButton>(find.byKey(const Key('view-save'))).onPressed,
      isNull,
    );
    await tester.enterText(find.byKey(const Key('view-name')), 'Gece nöbeti');
    await tester.pumpAndSettle();
    await tester.tap(find.text('Organizasyon'));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('view-save')));
    await tester.pumpAndSettle();

    expect(views.created.single['name'], 'Gece nöbeti');
    expect(views.created.single['visibility'], 'org');
    final state = views.created.single['state'] as Map<String, Object?>;
    expect(state['q'], 'timeout');
    expect(state['filters'], [
      {'key': 'severity_number', 'op': '>=', 'value': '13'},
    ]);
    // Saving closes the sheet, says it was kept, and names it on the
    // button -- the sheet closing on its own would look the same whether
    // the server took it or not. It does not also "apply" the view: the
    // state came off this screen a second ago.
    expect(find.byKey(const Key('view-name')), findsNothing);
    expect(find.byKey(const Key('view-saved')), findsOneWidget);
    expect(find.text('“Gece nöbeti” kaydedildi'), findsOneWidget);
    expect(find.text('Gece nöbeti'), findsOneWidget);
  });

  testWidgets('deleting asks first, and only where the server would allow it', (
    tester,
  ) async {
    final views = ScriptedViews(
      views: [
        savedView('v1'),
        savedView('v2', name: 'Başkasının görünümü', canEdit: false),
      ],
    );
    await pump(tester, views);

    await tester.tap(find.byKey(const Key('saved-views')));
    await tester.pumpAndSettle();

    // A view this account may not change has no buttons that would answer
    // 403; it can still be applied.
    expect(find.byKey(const Key('view-delete-v2')), findsNothing);
    expect(find.byKey(const Key('view-overwrite-v2')), findsNothing);

    await tester.tap(find.byKey(const Key('view-delete-v1')));
    await tester.pumpAndSettle();
    expect(views.removed, isEmpty);
    await tester.tap(find.byKey(const Key('view-delete-confirm-v1')));
    await tester.pumpAndSettle();
    expect(views.removed, ['v1']);
  });

  testWidgets('an installation without saved views shows no button', (
    tester,
  ) async {
    final views = ScriptedViews(unavailable: true);
    await pump(tester, views);

    expect(find.byKey(const Key('saved-views')), findsNothing);
  });

  testWidgets('a traces view moves its search into the conditions', (
    tester,
  ) async {
    final views = ScriptedViews(
      views: [
        savedView(
          'v1',
          name: 'Yavaş ödemeler',
          state: const {
            'filters': [
              {'key': 'service_name', 'op': 'contains', 'value': 'checkout'},
            ],
            'sort': 'duration',
            'root_only': true,
          },
        ),
      ],
    );
    final traces = ScriptedTraces();
    final sections = Sections(
      client: _client,
      traces: traces,
      traceViews: views,
    );
    addTearDown(sections.dispose);
    await tester.pumpWidget(
      MaterialApp(
        localizationsDelegates: L.localizationsDelegates,
        supportedLocales: L.supportedLocales,
        locale: const Locale('tr'),
        home: Scaffold(
          body: TracesBody(
            session: SessionController(store: MemoryTokenStore()),
            sections: sections,
            active: true,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    await tester.enterText(find.byKey(const Key('traces-search')), 'eski');
    await tester.testTextInput.receiveAction(TextInputAction.search);
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('saved-views')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('view-v1')));
    await tester.pumpAndSettle();

    // The search box is a contains over the service name underneath, so the
    // view's own search arrives as a condition and the box is emptied --
    // and the box on screen has to agree with that.
    expect(traces.query, '');
    expect([for (final f in traces.filters) f.op], ['contains']);
    expect(traces.slowest, isTrue);
    expect(
      tester
          .widget<TextField>(find.byKey(const Key('traces-search')))
          .controller
          ?.text,
      '',
    );
  });
}
