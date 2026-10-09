// The holiday calendars: the dates as typed, and what reaches the server.
//
// The parser is where the bugs would be -- "31-02" is a date that does not
// exist and `DateTime` happily turns it into March 3rd -- so it gets a test
// per shape rather than one happy path.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/calendar_dates.dart';
import 'package:openlog_mobile/src/sections.dart';

import 'fake_server.dart';

Map<String, Object?> calendar(
  String id, {
  String name = 'Resmi tatiller',
  List<String> dates = const ['01-01', '04-23'],
  int muteCount = 0,
}) => {
  'id': id,
  'name': name,
  'description': '',
  'dates': dates,
  'mute_count': muteCount,
  'created_by_email': 'owner@example.com',
  'created_at': '2026-10-01T09:00:00.000000000Z',
  'updated_at': '2026-10-01T09:00:00.000000000Z',
};

void main() {
  test('a date that does not exist is reported, not stored', () {
    // February 31st parses as March 3rd if it is only handed to DateTime.
    final parsed = parseCalendarDates('2026-02-31\n2026-02-28');
    expect(parsed.dates, ['2026-02-28']);
    expect(parsed.invalid, ['2026-02-31']);
  });

  test('yearly dates keep their shape; 02-29 is one of them', () {
    // `--MM-DD` is the iCalendar spelling of the same date and the server
    // stores the short one, so both have to arrive as `MM-DD`.
    final parsed = parseCalendarDates('--04-23, 02-29, 13-01, 04-23');
    expect(parsed.dates, ['02-29', '04-23']);
    expect(parsed.invalid, ['13-01']);
  });

  test('both separators, and the list comes back sorted', () {
    final parsed = parseCalendarDates(' 2026-05-19 ,2026-01-01\n2025-12-31 ');
    expect(parsed.dates, ['2025-12-31', '2026-01-01', '2026-05-19']);
    expect(parsed.invalid, isEmpty);
  });

  test('saving sends the parsed dates and an empty description', () async {
    final posted = <Map<String, Object?>>[];
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        posted.add(jsonDecode(seen.body) as Map<String, Object?>);
        writeJson(req, 201, calendar('c1'));
      } else {
        writeJson(req, 200, {'calendars': <Object>[]});
      }
    });
    addTearDown(server.stop);
    final c = AlertCalendarsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    final saved = await c.save(
      name: 'Resmi tatiller',
      description: '',
      dates: const ['01-01'],
    );

    expect(saved, isTrue);
    expect(posted.single['name'], 'Resmi tatiller');
    expect(posted.single['dates'], ['01-01']);
    // Sent although it is empty: an edit that clears the description has to
    // reach the server as an empty string, not as "leave it alone".
    expect(posted.single.containsKey('description'), isTrue);
  });

  test('a rejected save keeps the form open and says why', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        writeJson(req, 409, {
          'error': {'message': 'name already used'},
        });
      } else {
        writeJson(req, 200, {'calendars': <Object>[]});
      }
    });
    addTearDown(server.stop);
    final c = AlertCalendarsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    final saved = await c.save(
      name: 'Resmi tatiller',
      description: '',
      dates: const ['01-01'],
    );

    // False, so the form stays open with the dates somebody just typed; and
    // the server's own sentence, because "name already used" is the useful
    // part and this screen cannot word it better.
    expect(saved, isFalse);
    expect(c.failure?.kind, 'unexpected');
    expect(c.failure?.detail, contains('already used'));
  });

  test('a 409 on delete survives the reload that follows it', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'DELETE') {
        writeJson(req, 409, {
          'error': {'message': '2 mutes use it'},
        });
      } else {
        writeJson(req, 200, {
          'calendars': [calendar('c1', muteCount: 2)],
        });
      }
    });
    addTearDown(server.stop);
    final c = AlertCalendarsController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();
    await c.remove('c1');

    // The reload is what keeps the row; reporting before it would have the
    // successful refresh() throw the message away.
    expect(c.items.single.id, 'c1');
    expect(c.failure?.detail, contains('2 mutes use it'));
  });
}
