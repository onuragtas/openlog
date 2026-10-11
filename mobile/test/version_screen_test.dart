// The version screen: what it says about the updater, and to whom it offers
// the two buttons.
//
// Its own file because a suite that mixes `test` with `testWidgets` makes
// every real HTTP request answer 400, and the controller tests next door
// need a real one.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/ui/version_tab.dart';
import 'package:openlog_mobile/src/version.dart';

import 'version_test.dart' as data;

/// A controller that already has its answer, so the screen is what is tested.
class ScriptedVersion extends VersionController {
  ScriptedVersion(Map<String, Object?> json)
    : super(OpenlogClient(baseUrl: 'http://127.0.0.1:1')) {
    value = VersionInfo.fromJson(json);
    loaded = true;
  }

  @override
  Future<void> refresh() async {}
}

Future<void> pumpTab(WidgetTester tester, VersionController c) async {
  await tester.pumpWidget(
    MaterialApp(
      localizationsDelegates: L.localizationsDelegates,
      supportedLocales: L.supportedLocales,
      locale: const Locale('tr'),
      home: Scaffold(
        body: VersionTab(
          session: SessionController(store: MemoryTokenStore()),
          controller: c,
          active: true,
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  testWidgets("says a notice in this app's language", (tester) async {
    await pumpTab(
      tester,
      ScriptedVersion(
        data.info(
          updater: data.updater(
            notices: [
              {
                'code': 'compose_changes_pending',
                'message':
                    'the compose files of 0.1.113 change clickhouse in ways '
                    'the updater does not apply (volumes, ports, mounted '
                    'files): re-run install-server.sh or docker compose up -d',
                'params': {'version': '0.1.113', 'services': 'clickhouse'},
              },
            ],
          ),
        ),
      ),
    );

    // The server sends English with a stable code and the parameters beside
    // it; where this build knows the code it says it in Turkish.
    expect(
      find.textContaining('0.1.113 compose dosyaları clickhouse için'),
      findsOneWidget,
    );
  });

  testWidgets("a code this build does not know keeps the server's words", (
    tester,
  ) async {
    await pumpTab(
      tester,
      ScriptedVersion(
        data.info(
          updater: data.updater(
            notices: [
              {
                'code': 'something_newer',
                'message': 'a notice from a newer server',
                'params': <String, String>{},
              },
            ],
          ),
        ),
      ),
    );

    expect(find.text('a notice from a newer server'), findsOneWidget);
  });

  testWidgets('the buttons are not offered to a role that may not', (
    tester,
  ) async {
    await pumpTab(
      tester,
      ScriptedVersion(data.info(updater: data.updater(), canRequest: false)),
    );

    // Shown and refused is worse than not shown -- and the state is still
    // readable, which is what most people open this screen for.
    expect(find.byKey(const Key('version-check')), findsNothing);
    expect(find.text('0.1.131'), findsOneWidget);
    // And the updater's state is a word, not the wire's code -- twice over,
    // since "no newer release" and "the updater has nothing to do" are the
    // same word in Turkish.
    expect(find.text('Güncel'), findsNWidgets(2));
  });

  testWidgets('an update asks before it installs anything', (tester) async {
    await pumpTab(
      tester,
      ScriptedVersion(
        data.info(
          updater: data.updater(state: 'available'),
          latest: {
            'version': '0.1.132',
            'notes_url': '',
            'checked_at': '2026-10-11T10:00:00.000000000Z',
          },
        ),
      ),
    );

    await tester.tap(find.byKey(const Key('version-update')));
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('version-confirm')), findsOneWidget);
    expect(find.textContaining('0.1.132 kurulsun mu?'), findsOneWidget);
  });
}
