// The instance screen: the web's three tabs, in the web's order.
//
// A file of its own, because a widget test and a real-server test cannot
// share one: the test binding answers every real request with 400.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/ui/database_screen.dart';

void main() {
  testWidgets('the instance has the web\'s three tabs', (tester) async {
    final client = OpenlogClient(baseUrl: 'http://127.0.0.1:1');
    final sections = Sections(client: client);
    addTearDown(sections.dispose);
    await tester.pumpWidget(
      MaterialApp(
        localizationsDelegates: L.localizationsDelegates,
        supportedLocales: L.supportedLocales,
        locale: const Locale('tr'),
        home: DatabaseScreen(
          session: SessionController(store: MemoryTokenStore()),
          sections: sections,
          instance: 'db1.internal:5432',
          dbSystem: 'postgresql',
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(
      [for (final t in tester.widgetList<Tab>(find.byType(Tab))) t.text],
      ['Etkinlik', 'Sorgular', 'Oturumlar'],
    );
  });
}
