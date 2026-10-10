// Saved views: the state that goes out, and the state that comes back.
//
// This is a format shared with the web, so the tests are about the format:
// what a browser wrote has to arrive here as the same question, and what
// this app writes has to be readable there. The parts a phone cannot show
// are the interesting ones -- they must be kept, not dropped, and said out
// loud when they change which rows are listed.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/fields.dart';
import 'package:openlog_mobile/src/saved_views.dart';

import 'fake_server.dart';

Map<String, Object?> view(
  String id, {
  String name = 'Ödeme hataları',
  String signal = 'logs',
  String visibility = 'private',
  bool canEdit = true,
  Map<String, Object?> state = const {},
}) => {
  'id': id,
  'signal': signal,
  'name': name,
  'description': '',
  'visibility': visibility,
  'state': state,
  'created_by_user_id': 'u1',
  'created_by_email': 'owner@example.com',
  'can_edit': canEdit,
  'created_at': '2026-10-01T09:00:00.000000000Z',
  'updated_at': '2026-10-01T09:00:00.000000000Z',
};

void main() {
  test('a browser view reads as filters, a search and a severity', () {
    final state = ViewState.of(const {
      'filters': [
        {'key': 'service.name', 'op': '=', 'value': 'checkout'},
        {'key': 'severity_number', 'op': '>=', 'value': 17},
        {
          'key': 'http.status_code',
          'op': 'in',
          'values': [500, 503],
        },
      ],
      'q': 'timeout',
      'columns': ['timestamp', 'body'],
      'range': '24h',
    }, signal: 'logs');

    expect(state.query, 'timeout');
    // The severity button is a `severity_number >=` condition underneath, so
    // a browser's severity filter moves the button rather than sitting in a
    // chip that says the same thing twice.
    expect(state.severityMin, 'ERROR');
    expect(
      [for (final f in state.filters) f.key],
      ['service.name', 'http.status_code'],
    );
    // Numbers arrive as their text, which is what the server makes of them
    // as well.
    expect(state.filters.last.values, ['500', '503']);
    expect(state.partial, isFalse);
  });

  test('conditions this app could not send are dropped', () {
    final state = ViewState.of(const {
      'filters': [
        {'key': 'service.name', 'op': 'eq', 'value': 'checkout'},
        {'key': 'k', 'op': 'exists', 'value': 'x'},
        {'key': 'ok', 'op': 'contains', 'value': 'yes'},
        {'key': '', 'op': '=', 'value': 'v'},
        {
          'key': 'm',
          'op': '=',
          'value': {'a': 1},
        },
      ],
    }, signal: 'logs');

    // `eq` is not an operator the contract has; `exists` with a value is a
    // 400; a key-less condition and an object value are neither.
    expect([for (final f in state.filters) f.key], ['k', 'ok']);
    expect(state.filters.first.values, isEmpty);
  });

  test('a view whose question the phone cannot ask says so', () {
    final logs = ViewState.of(const {
      'filters': [
        {'key': 'service.name', 'op': '=', 'value': 'checkout'},
      ],
      'groups': [
        [
          {'key': 'severity_text', 'op': '=', 'value': 'ERROR'},
        ],
      ],
    }, signal: 'logs');
    expect(logs.hiddenGroups, 1);
    expect(logs.partial, isTrue);

    final traces = ViewState.of(const {
      'sort': 'duration',
      'root_only': false,
    }, signal: 'traces');
    expect(traces.slowest, isTrue);
    expect(traces.allSpans, isTrue);
    expect(traces.partial, isTrue);
  });

  test('what the phone cannot show, it keeps', () {
    final state = logsViewState(
      filters: const [
        Filter(key: 'service.name', op: '=', values: ['checkout']),
      ],
      query: 'timeout',
      severityMin: 'WARN',
      service: 'api',
      keep: const {
        'columns': ['timestamp', 'body'],
        'order': 'asc',
        'group_by': 'service.name',
        'range': '24h',
        // Replaced, not kept: this screen has no OR groups, so a view saved
        // from it has none.
        'groups': [
          [
            {'key': 'x', 'op': 'exists'},
          ],
        ],
      },
    );

    expect(state['columns'], ['timestamp', 'body']);
    expect(state['order'], 'asc');
    expect(state['group_by'], 'service.name');
    expect(state['range'], '24h');
    expect(state['groups'], isEmpty);
    expect(state['q'], 'timeout');
    // The service box and the severity button go out as plain conditions, so
    // a browser opening this view shows the same rows.
    expect(state['filters'], [
      {'key': 'service.name', 'op': '=', 'value': 'checkout'},
      {'key': 'service.name', 'op': '=', 'value': 'api'},
      {'key': 'severity_number', 'op': '>=', 'value': '13'},
    ]);
  });

  test('a traces view keeps its sort and says it listed root spans', () {
    final state = tracesViewState(
      filters: const [Filter(key: 'error', op: 'exists')],
      query: 'checkout',
      slowest: true,
      keep: const {
        'columns': ['timestamp', 'name'],
        'root_only': false,
      },
    );

    expect(state['sort'], 'duration');
    // What this app asked for, not what the old view asked for.
    expect(state['root_only'], isTrue);
    expect(state['filters'], [
      {'key': 'error', 'op': 'exists'},
      {'key': 'service_name', 'op': 'contains', 'value': 'checkout'},
    ]);
    expect(state['columns'], ['timestamp', 'name']);
  });

  test('a round trip through this app changes nothing it understands', () {
    final saved = logsViewState(
      filters: const [
        Filter(key: 'http.status_code', op: 'in', values: ['500', '503']),
      ],
      query: 'timeout',
      severityMin: 'ERROR',
    );
    final read = ViewState.of(saved, signal: 'logs');

    expect(read.query, 'timeout');
    expect(read.severityMin, 'ERROR');
    expect(read.filters.single.values, ['500', '503']);
    expect(read.partial, isFalse);
  });

  group('against a server', () {
    test('the signal is asked for, and the order is left alone', () async {
      final server = await FakeServer.start((req, seen) {
        writeJson(req, 200, {
          'views': [view('v1', name: 'Alarm'), view('v2', name: 'Zil')],
        });
      });
      addTearDown(server.stop);
      final c = SavedViewsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        signal: 'logs',
      );

      await c.load();

      expect(server.requests.single.query, contains('signal=logs'));
      // The server sorts by name; reshuffling it here would only make the
      // phone's list differ from the browser's.
      expect([for (final v in c.views) v.name], ['Alarm', 'Zil']);
      expect(c.unavailable, isFalse);
    });

    test(
      'an installation without views hides them, rather than failing',
      () async {
        final server = await FakeServer.start((req, seen) {
          writeJson(req, 404, {
            'error': {'code': 'not_found', 'message': 'not found'},
          });
        });
        addTearDown(server.stop);
        final c = SavedViewsController(
          OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
          signal: 'logs',
        );

        await c.load();

        // 404 here is "this installation keeps no views", which is a different
        // answer from "you have none" and from "something went wrong".
        expect(c.unavailable, isTrue);
        expect(c.failure, isNull);
        expect(c.loaded, isTrue);
      },
    );

    test('saving sends the signal, the visibility and the state', () async {
      final bodies = <Map<String, Object?>>[];
      final server = await FakeServer.start((req, seen) {
        if (seen.method == 'POST') {
          bodies.add(jsonDecode(seen.body) as Map<String, Object?>);
          writeJson(req, 201, view('v9', name: 'Ödeme hataları'));
        } else {
          writeJson(req, 200, {
            'views': [
              view('v9', name: 'Ödeme hataları'),
              view('v1', name: 'Zil'),
            ],
          });
        }
      });
      addTearDown(server.stop);
      final c = SavedViewsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        signal: 'logs',
      );

      c.views = [SavedView.fromJson(view('v1', name: 'Zil'))];
      final made = await c.create(
        name: 'Ödeme hataları',
        visibility: 'org',
        state: const {'q': 'timeout'},
      );

      expect(made?.id, 'v9');
      expect(bodies.single, {
        'signal': 'logs',
        'name': 'Ödeme hataları',
        'visibility': 'org',
        'state': {'q': 'timeout'},
      });
      // The new view is the one on screen afterwards, as it is on the web,
      // and the list is asked for again rather than guessed at -- the order
      // is the server's.
      expect(c.activeId, 'v9');
      expect([for (final v in c.views) v.name], ['Ödeme hataları', 'Zil']);
      expect(server.requests.last.method, 'GET');
    });

    test('overwriting keeps the name and who may see it', () async {
      final bodies = <Map<String, Object?>>[];
      final server = await FakeServer.start((req, seen) {
        if (seen.method == 'GET') {
          writeJson(req, 200, {'views': <Object>[]});
          return;
        }
        bodies.add(jsonDecode(seen.body) as Map<String, Object?>);
        writeJson(
          req,
          200,
          view('v1', name: 'Zil', visibility: 'org', state: {'q': 'yeni'}),
        );
      });
      addTearDown(server.stop);
      final client = OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x';
      final c = SavedViewsController(client, signal: 'logs');

      await c.overwrite(
        SavedView.fromJson(view('v1', name: 'Zil', visibility: 'org')),
        const {'q': 'yeni'},
      );

      expect(server.requests.first.method, 'PUT');
      expect(bodies.single['name'], 'Zil');
      expect(bodies.single['visibility'], 'org');
      expect(bodies.single['state'], {'q': 'yeni'});
    });

    test('a full organization is told it is full', () async {
      final server = await FakeServer.start((req, seen) {
        writeJson(req, 409, {
          'error': {
            'code': 'failed_precondition',
            'message': 'the organization already has the maximum number',
          },
        });
      });
      addTearDown(server.stop);
      final c = SavedViewsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        signal: 'logs',
      );

      final made = await c.create(
        name: 'Bir daha',
        visibility: 'private',
        state: const {},
      );

      expect(made, isNull);
      expect(c.failure?.kind, 'viewsFull');
    });

    test('deleting takes the row out of the list', () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.method == 'GET') {
          writeJson(req, 200, {
            'views': [view('v2')],
          });
          return;
        }
        req.response.statusCode = 204;
      });
      addTearDown(server.stop);
      final c = SavedViewsController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
        signal: 'logs',
      );
      c.views = [
        SavedView.fromJson(view('v1')),
        SavedView.fromJson(view('v2')),
      ];
      c.activeId = 'v1';

      expect(await c.remove('v1'), isTrue);

      expect(server.requests.first.path, endsWith('/saved-views/v1'));
      expect([for (final v in c.views) v.id], ['v2']);
      // The button must not keep naming a view that is gone.
      expect(c.activeId, isNull);
    });
  });
}
