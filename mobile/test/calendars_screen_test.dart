// The holiday calendars screen: what it refuses to send, and what it asks
// before deleting.
//
// Scripted controller rather than a server, like the other widget tests: the
// test binding answers every real request with a 400, and what is worth
// testing here is the screen anyway.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/ui/calendars_screen.dart';

AlertHolidayCalendar calendar(
  String id, {
  String name = 'Resmi tatiller',
  List<String> dates = const ['01-01', '04-23'],
  int muteCount = 0,
}) => AlertHolidayCalendar(
  id: id,
  name: name,
  description: '',
  dates: dates,
  muteCount: muteCount,
  createdByEmail: 'owner@example.com',
  createdAt: DateTime.utc(2026, 10, 1, 9),
  updatedAt: DateTime.utc(2026, 10, 1, 9),
);

class ScriptedCalendars extends AlertCalendarsController {
  ScriptedCalendars({List<AlertHolidayCalendar> calendars = const []})
    : super(OpenlogClient(baseUrl: 'http://127.0.0.1:1')) {
    items = calendars;
    loaded = true;
  }

  final calls = <String>[];

  @override
  Future<void> refresh() async => calls.add('refresh');

  @override
  Future<void> remove(String id) async => calls.add('remove:$id');

  @override
  Future<bool> save({
    String? id,
    required String name,
    required String description,
    required List<String> dates,
  }) async {
    calls.add('save:${id ?? 'new'}:$name:${dates.join('|')}');
    return true;
  }
}

Future<void> pump(WidgetTester tester, ScriptedCalendars c) async {
  await tester.pumpWidget(
    MaterialApp(
      localizationsDelegates: L.localizationsDelegates,
      supportedLocales: L.supportedLocales,
      locale: const Locale('tr'),
      home: CalendarsScreen(
        session: SessionController(store: MemoryTokenStore()),
        calendars: c,
      ),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('the save button waits for a name and for valid dates', (
    tester,
  ) async {
    final c = ScriptedCalendars();
    addTearDown(c.dispose);
    await pump(tester, c);
    await tester.tap(find.byKey(const Key('calendar-new')));
    await tester.pumpAndSettle();

    FilledButton save() =>
        tester.widget<FilledButton>(find.byKey(const Key('calendar-save')));

    // Nothing typed: nothing to send.
    expect(save().onPressed, isNull);

    await tester.enterText(find.byKey(const Key('calendar-name')), 'Tatiller');
    await tester.enterText(
      find.byKey(const Key('calendar-dates')),
      '01-01\n31-02',
    );
    await tester.pumpAndSettle();

    // February 31st is impossible, and the screen names it rather than
    // dropping it: a calendar quietly missing a day is a mute that fires on a
    // holiday.
    expect(find.textContaining('31-02'), findsWidgets);
    expect(save().onPressed, isNull);

    await tester.enterText(find.byKey(const Key('calendar-dates')), '01-01');
    await tester.pumpAndSettle();
    expect(save().onPressed, isNotNull);
    expect(find.text('1 tarih'), findsOneWidget);

    await tester.tap(find.byKey(const Key('calendar-save')));
    await tester.pumpAndSettle();
    expect(c.calls, ['save:new:Tatiller:01-01']);
  });

  testWidgets('editing an existing calendar sends its id and its dates', (
    tester,
  ) async {
    final c = ScriptedCalendars(calendars: [calendar('c1')]);
    addTearDown(c.dispose);
    await pump(tester, c);
    await tester.tap(find.byKey(const Key('calendar-edit-c1')));
    await tester.pumpAndSettle();

    // The fields open on what the calendar has, so an edit is an edit rather
    // than retyping the list.
    expect(find.text('01-01\n04-23'), findsOneWidget);

    await tester.tap(find.byKey(const Key('calendar-save')));
    await tester.pumpAndSettle();
    expect(c.calls, ['save:c1:Resmi tatiller:01-01|04-23']);
  });

  testWidgets('deleting asks first, and says how many mutes use it', (
    tester,
  ) async {
    final c = ScriptedCalendars(calendars: [calendar('c1', muteCount: 3)]);
    addTearDown(c.dispose);
    await pump(tester, c);

    await tester.tap(find.byKey(const Key('calendar-delete-c1')));
    await tester.pumpAndSettle();

    // The dialog says what the row's count means: the server refuses the
    // delete while a mute references the calendar. Scoped to the dialog
    // because the row behind it says "3 susturmada kullanılıyor" too.
    expect(
      find.descendant(
        of: find.byType(AlertDialog),
        matching: find.textContaining('3 susturma'),
      ),
      findsOneWidget,
    );
    expect(c.calls, isEmpty);

    await tester.tap(find.byKey(const Key('calendar-confirm')));
    await tester.pumpAndSettle();
    expect(c.calls, ['remove:c1']);
  });
}
