// The service screen's tabs: the web's six, in the web's order, and each
// one asked for only when somebody looks at it.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/detail.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/services.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/ui/service_screen.dart';

final client = OpenlogClient(baseUrl: 'http://127.0.0.1:1');

class ScriptedOverview extends ServiceOverviewController {
  ScriptedOverview() : super(client, 'checkout');
  final calls = <String>[];
  @override
  Future<void> refresh() async {
    calls.add('overview');
    // Like a real one: loaded, so the tab listener firing twice per change
    // does not ask twice.
    loaded = true;
  }
}

class ScriptedTransactions extends ServiceTransactionsController {
  ScriptedTransactions() : super(client, 'checkout');
  final calls = <String>[];
  @override
  Future<void> refresh() async {
    calls.add('transactions');
    loaded = true;
  }
}

class ScriptedErrors extends ServiceErrorsController {
  ScriptedErrors() : super(client, 'checkout');
  final calls = <String>[];
  @override
  Future<void> refresh() async {
    calls.add('errors');
    loaded = true;
  }
}

class ScriptedDatabases extends ServiceDatabasesController {
  ScriptedDatabases() : super(client, 'checkout');
  final calls = <String>[];
  @override
  Future<void> refresh() async {
    calls.add('databases');
    loaded = true;
  }
}

class ScriptedMap extends ServiceMapController {
  ScriptedMap() : super(client, 'checkout');
  final calls = <String>[];
  @override
  Future<void> refresh() async {
    calls.add('map');
    loaded = true;
  }
}

class ScriptedTraces extends ServiceTracesController {
  ScriptedTraces() : super(client, 'checkout');
  final calls = <String>[];
  @override
  Future<void> refresh() async {
    calls.add('traces');
    loaded = true;
  }
}

void main() {
  testWidgets('the tabs are the web\'s, in the web\'s order', (tester) async {
    final sections = Sections(
      client: client,
      serviceOverview: (_) => ScriptedOverview(),
      serviceTransactions: (_) => ScriptedTransactions(),
      serviceErrors: (_) => ScriptedErrors(),
      serviceDatabases: (_) => ScriptedDatabases(),
      serviceMap: (_) => ScriptedMap(),
      serviceTraces: (_) => ScriptedTraces(),
    );

    await tester.pumpWidget(
      MaterialApp(
        localizationsDelegates: L.localizationsDelegates,
        supportedLocales: L.supportedLocales,
        home: ServiceScreen(
          session: SessionController(store: MemoryTokenStore()),
          sections: sections,
          serviceName: 'checkout',
        ),
      ),
    );
    await tester.pumpAndSettle();

    final tabs = [
      for (final w in tester.widgetList<Tab>(find.byType(Tab))) w.text ?? '',
    ];
    expect(tabs, [
      'Overview',
      'Transactions',
      'Errors',
      'Databases',
      'Map',
      'Traces',
    ]);
  });

  testWidgets('a tab asks the server the first time it is looked at', (
    tester,
  ) async {
    final transactions = ScriptedTransactions();
    final databases = ScriptedDatabases();
    final overview = ScriptedOverview();
    final sections = Sections(
      client: client,
      serviceOverview: (_) => overview,
      serviceTransactions: (_) => transactions,
      serviceErrors: (_) => ScriptedErrors(),
      serviceDatabases: (_) => databases,
      serviceMap: (_) => ScriptedMap(),
      serviceTraces: (_) => ScriptedTraces(),
    );

    await tester.pumpWidget(
      MaterialApp(
        localizationsDelegates: L.localizationsDelegates,
        supportedLocales: L.supportedLocales,
        home: ServiceScreen(
          session: SessionController(store: MemoryTokenStore()),
          sections: sections,
          serviceName: 'checkout',
        ),
      ),
    );
    await tester.pumpAndSettle();

    // Every tab is built at once by TabBarView, so building cannot be the
    // signal: opening a service would ask the server six questions. The
    // transactions are the exception -- the overview shows the top of them,
    // as the web's does, so they come with it.
    expect(overview.calls, ['overview']);
    expect(transactions.calls, ['transactions']);
    expect(databases.calls, isEmpty);

    await tester.tap(find.byKey(const Key('tab-transactions')));
    await tester.pumpAndSettle();
    // Not asked again: the overview already has them.
    expect(transactions.calls, ['transactions']);
    expect(databases.calls, isEmpty);

    await tester.tap(find.byKey(const Key('tab-databases')));
    await tester.pumpAndSettle();
    expect(databases.calls, ['databases']);
  });
}
