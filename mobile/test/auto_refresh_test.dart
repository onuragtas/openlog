// Asking again by itself: the interval, and the four reasons not to tick.
import 'package:fake_async/fake_async.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/auto_refresh.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/prefs.dart';
import 'package:openlog_mobile/src/time_range.dart';
import 'package:openlog_mobile/src/ui/app_shell.dart';

import 'app_test.dart' show ScriptedAlerts, ScriptedSession, me;

void main() {
  group('the chosen interval', () {
    test('is read as milliseconds, and off is off', () {
      final auto = AutoRefreshController();
      expect(auto.on, isFalse);
      expect(auto.everyMs, isNull);

      auto.choose('30s');
      expect(auto.everyMs, 30000);
      auto.choose('5m');
      expect(auto.everyMs, 300000);

      // Anything that is not one of the web's six means off, so a stored
      // value from a future version cannot make a phone ask every 1ms.
      auto.choose('1ms');
      expect(auto.on, isFalse);
    });

    test('is remembered on this device, and junk is ignored', () async {
      final prefs = MemoryPrefs();
      final auto = AutoRefreshController(prefs: prefs);
      await auto.choose('10s');
      expect(await prefs.read(prefAutoRefresh), '10s');

      final next = AutoRefreshController(prefs: prefs);
      await next.load();
      expect(next.interval, '10s');

      final bad = AutoRefreshController(
        prefs: MemoryPrefs({prefAutoRefresh: '1s'}),
      );
      await bad.load();
      expect(bad.on, isFalse);
    });
  });

  group('the timer', () {
    test('ticks, skips while busy, and stops in the background', () {
      fakeAsync((async) {
        var ticks = 0;
        var busy = false;
        final timer = AutoRefreshTimer(
          refresh: () => ticks++,
          busy: () => busy,
        );
        addTearDown(timer.stop);

        timer.start(5000);
        async.elapse(const Duration(seconds: 11));
        expect(ticks, 2);

        // A request is still running: the tick is skipped, not queued, so a
        // slow answer on a phone network cannot pile requests on itself.
        busy = true;
        async.elapse(const Duration(seconds: 10));
        expect(ticks, 2);
        busy = false;

        // Nobody is looking at it.
        timer.setForeground(false);
        async.elapse(const Duration(seconds: 10));
        expect(ticks, 2);
        timer.setForeground(true);
        async.elapse(const Duration(seconds: 5));
        expect(ticks, 3);

        timer.stop();
        async.elapse(const Duration(minutes: 1));
        expect(ticks, 3);
        expect(timer.running, isFalse);
      });
    });

    test('starting again re-spaces it rather than running two', () {
      fakeAsync((async) {
        var ticks = 0;
        final timer = AutoRefreshTimer(
          refresh: () => ticks++,
          busy: () => false,
        );
        addTearDown(timer.stop);
        timer.start(5000);
        timer.start(60000);
        async.elapse(const Duration(seconds: 30));
        expect(ticks, 0);
        async.elapse(const Duration(seconds: 30));
        expect(ticks, 1);
      });
    });
  });

  group('the shell', () {
    Future<(ScriptedAlerts, Sections)> pumpShell(
      WidgetTester tester, {
      Prefs? prefs,
    }) async {
      final alerts = ScriptedAlerts();
      final sections = Sections(
        client: OpenlogClient(baseUrl: 'http://127.0.0.1:1'),
        alerts: alerts,
      );
      addTearDown(sections.dispose);
      await tester.pumpWidget(
        MaterialApp(
          localizationsDelegates: L.localizationsDelegates,
          supportedLocales: L.supportedLocales,
          locale: const Locale('tr'),
          home: AppShell(
            session: ScriptedSession(stage: SessionStage.signedIn)..me = me(),
            sections: sections,
            autoRefresh: AutoRefreshController(prefs: prefs ?? MemoryPrefs()),
          ),
        ),
      );
      await tester.pumpAndSettle();
      alerts.calls.clear();
      return (alerts, sections);
    }

    testWidgets('does not ask again until an interval is chosen', (
      tester,
    ) async {
      final (alerts, _) = await pumpShell(tester);
      await tester.pump(const Duration(minutes: 2));
      expect(alerts.calls, isEmpty);

      await tester.tap(find.byKey(const Key('refresh-menu')));
      await tester.pumpAndSettle();
      await tester.tap(find.text('30s').last);
      await tester.pumpAndSettle();

      await tester.pump(const Duration(seconds: 30));
      await tester.pumpAndSettle();
      expect(alerts.calls, ['refresh']);
      await tester.pump(const Duration(seconds: 30));
      await tester.pumpAndSettle();
      expect(alerts.calls, ['refresh', 'refresh']);
    });

    testWidgets('starts on the interval this device chose last time', (
      tester,
    ) async {
      final (alerts, _) = await pumpShell(
        tester,
        prefs: MemoryPrefs({prefAutoRefresh: '5s'}),
      );
      await tester.pump(const Duration(seconds: 5));
      await tester.pumpAndSettle();
      expect(alerts.calls, ['refresh']);
    });

    testWidgets('stops for a window with two fixed ends', (tester) async {
      final (alerts, sections) = await pumpShell(
        tester,
        prefs: MemoryPrefs({prefAutoRefresh: '5s'}),
      );
      sections.range.choose(
        TimeRange.absolute(
          DateTime.utc(2026, 10, 1),
          DateTime.utc(2026, 10, 2),
        ),
      );
      await tester.pumpAndSettle();

      // Refetching a window that does not move asks the same question
      // again, so the web disables the control and so does this.
      await tester.pump(const Duration(minutes: 1));
      expect(alerts.calls, isEmpty);

      await tester.tap(find.byKey(const Key('refresh-menu')));
      await tester.pumpAndSettle();
      expect(find.text('Sabit aralıkta otomatik yenileme kapalı'), findsOne);
      // Refresh now still works: it is the window that is fixed, not the
      // data in it.
      await tester.tap(find.text('Şimdi yenile'));
      await tester.pumpAndSettle();
      expect(alerts.calls, ['refresh']);
    });
  });
}
