// A mute: what state it is in, the rule a form writes, what gets sent, and an
// older server that cannot edit or preview. The rule examples are the ones in
// web/src/lib/mute-schedule.ts, so the two consoles are held to one answer.
import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/infra_detail.dart';
import 'package:openlog_mobile/src/mute_schedule.dart';
import 'package:openlog_mobile/src/roles.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/ui/mute_form.dart';

import 'fake_server.dart';

const _t = '2026-10-28T09:00:00.000000000Z';

const _noEndpoint = {
  'error': {'code': 'not_found', 'message': 'no such endpoint'},
};

Map<String, Object?> _mute({
  bool active = false,
  String starts = _t,
  String ends = '2026-10-28T11:00:00.000000000Z',
  Map<String, Object?>? schedule,
}) => {
  'id': 'm1',
  'name': 'Deploy',
  'comment': 'checkout',
  'starts_at': starts,
  'ends_at': ends,
  'rule_ids': ['r1'],
  'matchers': [
    {'label': 'host.name', 'op': 'eq', 'value': 'web-1'},
  ],
  'schedule': schedule,
  'upcoming': <Object>[],
  'active': active,
  'created_by_user_id': 'u1',
  'created_by_email': 'a@b',
  'created_at': _t,
  'updated_at': _t,
};

Map<String, Object?> _schedule({
  List<String> days = const ['mon', 'tue'],
  String? rrule,
}) => {
  'timezone': 'Europe/Istanbul',
  'days': days,
  'rrule': rrule,
  'start_time': '22:00',
  'end_time': '06:00',
  'from': _t,
  'until': null,
  'exdates': ['2026-10-29'],
  'holiday_calendar_ids': ['c1'],
};

void main() {
  // testWidgets swaps in a binding that answers every request with a 400, and
  // these talk to a real loopback server.
  setUpAll(() => HttpOverrides.global = null);

  group('its status', () {
    final now = DateTime.utc(2026, 10, 28, 10);

    test('the server saying active wins', () {
      final m = AlertMute.fromJson(
        _mute(active: true, ends: '2020-01-01T00:00:00.000000000Z'),
      );
      expect(muteStatus(m, now), MuteStatus.active);
    });

    test('a one-off reads by its window', () {
      MuteStatus at(String s, String e) =>
          muteStatus(AlertMute.fromJson(_mute(starts: s, ends: e)), now);
      expect(
        at('2026-10-28T11:00:00.000000000Z', '2026-10-28T12:00:00.000000000Z'),
        MuteStatus.scheduled,
      );
      expect(
        at('2026-10-28T09:00:00.000000000Z', '2026-10-28T11:00:00.000000000Z'),
        MuteStatus.active,
      );
      expect(
        at('2026-10-28T07:00:00.000000000Z', '2026-10-28T08:00:00.000000000Z'),
        MuteStatus.ended,
      );
    });

    test('a schedule not silencing now is scheduled until it ends', () {
      final m = AlertMute.fromJson(_mute(schedule: _schedule()));
      expect(muteStatus(m, now), MuteStatus.scheduled);
      expect(muteStatus(m, DateTime.utc(2027)), MuteStatus.ended);
    });
  });

  group('its rule', () {
    test('month days parse, sort and refuse nonsense', () {
      expect(parseMonthDays('15, 1, -1, 1'), [-1, 1, 15]);
      expect(parseMonthDays(''), isNull);
      expect(parseMonthDays('0'), isNull);
      expect(parseMonthDays('32'), isNull);
      expect(parseMonthDays('x'), isNull);
    });

    test('a form writes the subset the API accepts', () {
      expect(
        monthlyRRule(const MonthlyForm(monthDays: '1, 15')),
        'FREQ=MONTHLY;BYMONTHDAY=1,15',
      );
      expect(monthlyRRule(const MonthlyForm(monthDays: '')), isNull);
      expect(
        monthlyRRule(
          const MonthlyForm(mode: 'weekday', ordinal: -1, day: 'fri'),
        ),
        'FREQ=MONTHLY;BYDAY=-1FR',
      );
      expect(
        monthlyRRule(
          const MonthlyForm(mode: 'weekday', ordinal: 2, day: anyWeekday),
        ),
        'FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=2',
      );
    });

    test('a stored rule reads back into the form', () {
      final a = parseMonthlyRRule('FREQ=MONTHLY;BYMONTHDAY=1,15')!;
      expect((a.mode, a.monthDays), ('monthday', '1, 15'));
      final b = parseMonthlyRRule('FREQ=MONTHLY;BYDAY=-1FR')!;
      expect((b.mode, b.ordinal, b.day), ('weekday', -1, 'fri'));
      final c = parseMonthlyRRule(
        'FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1',
      )!;
      expect((c.mode, c.ordinal, c.day), ('weekday', -1, anyWeekday));
    });

    test('a rule the form cannot represent is not a form', () {
      expect(parseMonthlyRRule('FREQ=WEEKLY;BYDAY=MO'), isNull);
      expect(parseMonthlyRRule('FREQ=MONTHLY;BYDAY=MO;BYSETPOS=9'), isNull);
      expect(parseMonthlyRRule(null), isNull);
    });
  });

  group('what the form sends', () {
    test('a one-off clears any schedule and keeps what it did not touch', () {
      final d = MuteDraft.from(AlertMute.fromJson(_mute()));
      final input = d.toInput();

      expect(input['name'], 'Deploy');
      expect(input['comment'], 'checkout');
      expect(input['rule_ids'], ['r1']);
      expect(input['schedule'], isNull);
      expect(input.containsKey('schedule'), isTrue);
      expect(input['starts_at'], '2026-10-28T09:00:00.000Z');
      expect((input['matchers']! as List).single, {
        'label': 'host.name',
        'op': 'eq',
        'value': 'web-1',
      });
    });

    test('a matcher with no label is not sent', () {
      final d = MuteDraft.from(AlertMute.fromJson(_mute()))
        ..matchers = [MuteMatcherDraft('  ')];
      expect(d.toInput()['matchers'], isEmpty);
    });

    test('a schedule goes back with its exceptions and calendars', () {
      final d = MuteDraft.from(
        AlertMute.fromJson(_mute(schedule: _schedule())),
      );
      final input = d.toInput();
      final s = input['schedule']! as Map<String, Object?>;

      expect(input.containsKey('starts_at'), isFalse);
      expect(s['days'], ['mon', 'tue']);
      expect(s.containsKey('rrule'), isFalse);
      expect(s['exdates'], ['2026-10-29']);
      expect(s['holiday_calendar_ids'], ['c1']);
      expect(s['timezone'], 'Europe/Istanbul');
    });

    test('a custom monthly rule survives the trip unchanged', () {
      const custom = 'FREQ=MONTHLY;BYMONTHDAY=1;BYSETPOS=2';
      final d = MuteDraft.from(
        AlertMute.fromJson(
          _mute(
            schedule: _schedule(days: const [], rrule: custom),
          ),
        ),
      );

      expect(d.customRRule, custom);
      expect(d.recurrence, 'monthly');
      expect((d.schedule)['rrule'], custom);
    });

    test('bad month days hold the save back, and the preview too', () {
      final d = MuteDraft.from(null)
        ..recurring = true
        ..recurrence = 'monthly'
        ..monthly = const MonthlyForm(monthDays: 'nope');

      expect(d.monthDaysInvalid, isTrue);
      expect(d.previewable, isFalse);
    });

    test('a new one starts now, for two hours, on weekdays', () {
      final now = DateTime(2026, 10, 28, 9);
      final d = MuteDraft.from(null, now: now);

      expect(d.starts, now);
      expect(d.ends, now.add(const Duration(hours: 2)));
      expect(d.days, ['mon', 'tue', 'wed', 'thu', 'fri']);
      expect(d.startTime, '22:00');
      expect(d.endTime, '06:00');
    });
  });

  group('who may edit', () {
    test('an admin any, a member their own, a viewer none', () {
      expect(canEditOwnedMute(Role.admin, 'x', 'u'), isTrue);
      expect(canEditOwnedMute(Role.member, 'u', 'u'), isTrue);
      expect(canEditOwnedMute(Role.member, 'x', 'u'), isFalse);
      expect(canEditOwnedMute(Role.member, null, 'u'), isFalse);
      expect(canEditOwnedMute(Role.viewer, 'u', 'u'), isFalse);
    });
  });

  group('saving', () {
    test('an edit is a PUT, and the list reloads', () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.method == 'PUT') {
          writeJson(req, 200, _mute());
        } else {
          writeJson(req, 200, {'mutes': <Object>[]});
        }
      });
      addTearDown(server.stop);
      final c = AlertMutesController(OpenlogClient(baseUrl: server.baseUrl));

      final saved = await c.save('m1', {'name': 'x'});

      expect(saved, isTrue);
      final put = server.requests.firstWhere((r) => r.method == 'PUT');
      expect(put.path, '/api/v1/alerts/mutes/m1');
      expect(jsonDecode(put.body), {'name': 'x'});
      expect(server.requests.last.query, 'include_expired=true');
    });

    test('a new one is a POST', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, {'mutes': <Object>[]}),
      );
      addTearDown(server.stop);
      final c = AlertMutesController(OpenlogClient(baseUrl: server.baseUrl));

      await c.save(null, {'name': 'x'});

      expect(server.requests.first.method, 'POST');
    });

    test('an older server cannot edit, and that is no failure', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 404, _noEndpoint),
      );
      addTearDown(server.stop);
      final c = AlertMutesController(OpenlogClient(baseUrl: server.baseUrl));

      expect(await c.save('m1', {'name': 'x'}), isFalse);
      expect(c.saveNotOffered, isTrue);
      expect(c.saveFailure, isNull);
    });

    test('a mute that is gone says so', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 404, {
          'error': {'code': 'not_found', 'message': 'mute not found'},
        }),
      );
      addTearDown(server.stop);
      final c = AlertMutesController(OpenlogClient(baseUrl: server.baseUrl));

      expect(await c.save('m1', {'name': 'x'}), isFalse);
      expect(c.saveNotOffered, isFalse);
      expect(c.saveFailure?.kind, 'detailGone');
    });
  });

  group('the preview', () {
    test('asks about the schedule and reads the occurrences', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, {
          'occurrences': [
            {'starts_at': _t, 'ends_at': _t},
          ],
        }),
      );
      addTearDown(server.stop);
      final p = MutePreviewController(OpenlogClient(baseUrl: server.baseUrl));

      await p.run({'start_time': '22:00'});

      expect(server.requests.single.path, '/api/v1/alerts/mutes/preview');
      expect(jsonDecode(server.requests.single.body), {
        'schedule': {'start_time': '22:00'},
      });
      expect(p.occurrences, hasLength(1));
    });

    test('an older server has none, and that is no error', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 404, _noEndpoint),
      );
      addTearDown(server.stop);
      final p = MutePreviewController(OpenlogClient(baseUrl: server.baseUrl));

      await p.run({});

      expect(p.notOffered, isTrue);
      expect(p.error, isNull);
    });

    test('a refused schedule says why', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 400, {
          'error': {'code': 'invalid', 'message': 'unknown time zone'},
        }),
      );
      addTearDown(server.stop);
      final p = MutePreviewController(OpenlogClient(baseUrl: server.baseUrl));

      await p.run({});

      expect(p.error, 'unknown time zone');
      expect(p.notOffered, isFalse);
    });
  });

  group('on a phone', () {
    Future<void> settle(WidgetTester tester) async {
      for (var i = 0; i < 5; i++) {
        await tester.pump(const Duration(milliseconds: 600));
      }
    }

    /// A real server cannot answer inside a widget test's fake clock.
    Future<void> pumpForm(
      WidgetTester tester,
      AlertMute? mute, {
      required bool preview,
    }) async {
      tester.view.physicalSize = const Size(390, 1600);
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.reset);
      final sections = Sections(
        client: OpenlogClient(
          baseUrl: 'http://x',
          httpClient: MockClient((request) async {
            final path = request.url.path;
            final Object body;
            var status = 200;
            if (path.endsWith('/preview')) {
              if (preview) {
                body = {
                  'occurrences': [
                    {
                      'starts_at': '2026-10-29T19:00:00.000000000Z',
                      'ends_at': '2026-10-30T03:00:00.000000000Z',
                    },
                  ],
                };
              } else {
                body = _noEndpoint;
                status = 404;
              }
            } else if (path.endsWith('/rules')) {
              body = {'rules': <Object>[]};
            } else if (path.endsWith('/holiday-calendars')) {
              body = {'calendars': <Object>[]};
            } else {
              body = {'mutes': <Object>[]};
            }
            return http.Response(jsonEncode(body), status);
          }),
        ),
      );
      addTearDown(sections.dispose);
      await tester.pumpWidget(
        MaterialApp(
          localizationsDelegates: L.localizationsDelegates,
          supportedLocales: L.supportedLocales,
          locale: const Locale('en'),
          home: MuteFormScreen(
            session: SessionController(store: MemoryTokenStore()),
            sections: sections,
            mute: mute,
          ),
        ),
      );
    }

    testWidgets('a recurring mute shows where it lands next', (tester) async {
      await pumpForm(
        tester,
        AlertMute.fromJson(_mute(schedule: _schedule())),
        preview: true,
      );
      await settle(tester);

      expect(find.byKey(const Key('mute-form-day-mon')), findsOneWidget);
      expect(find.textContaining('2026-10-'), findsWidgets);
      expect(find.text('Save mute'), findsOneWidget);
    });

    testWidgets('a server without the preview still lets it be saved', (
      tester,
    ) async {
      await pumpForm(
        tester,
        AlertMute.fromJson(_mute(schedule: _schedule())),
        preview: false,
      );
      await settle(tester);

      expect(find.byKey(const Key('not-offered')), findsOneWidget);
      final save = tester.widget<FilledButton>(
        find.byKey(const Key('mute-form-save')),
      );
      expect(save.onPressed, isNotNull);
    });

    testWidgets('a one-off shows its window and no preview', (tester) async {
      await pumpForm(tester, AlertMute.fromJson(_mute()), preview: true);
      await settle(tester);

      expect(find.byKey(const Key('mute-form-starts')), findsOneWidget);
      expect(find.byKey(const Key('mute-upcoming')), findsNothing);
    });
  });
}
