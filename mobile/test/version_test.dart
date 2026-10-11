// What this installation runs, and what the updater is doing about it.
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/version.dart';

import 'fake_server.dart';

Map<String, Object?> info({
  Map<String, Object?>? updater,
  Map<String, Object?>? latest,
  bool canRequest = true,
  Map<String, Object?>? request,
}) => {
  'version': '0.1.131',
  'commit': '468bc7104e9e',
  'date': '2026-10-10T22:39:19Z',
  'update_check': 'enabled',
  'latest_available': latest,
  'updater': updater,
  'update_requests': {
    'can_request': canRequest,
    'updater_listening': true,
    'updater_polled_at': null,
    'latest': request,
  },
};

Map<String, Object?> updater({
  String state = 'up_to_date',
  List<Object?>? notices,
  List<Object?>? steps,
}) => {
  'engine': 'compose',
  'mode': 'auto',
  'state': state,
  'checked_at': '2026-10-11T09:30:00.000000000Z',
  'notices': notices,
  'steps': steps,
};

void main() {
  test('the running build, the release available and the updater', () async {
    final server = await FakeServer.start(
      (req, seen) => writeJson(
        req,
        200,
        info(
          updater: updater(),
          latest: {
            'version': '0.1.132',
            'notes_url': 'https://example.test/v0.1.132',
            'checked_at': '2026-10-11T10:00:00.000000000Z',
          },
        ),
      ),
    );
    addTearDown(server.stop);
    final c = VersionController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();

    expect(c.value!.version, '0.1.131');
    expect(c.value!.latestAvailable!.version, '0.1.132');
    // There is a release and an updater that may install it.
    expect(updateTarget(c.value), '0.1.132');
    expect(canRequestUpdates(c.value), isTrue);
  });

  test('nothing is offered to install without an updater', () async {
    final server = await FakeServer.start(
      (req, seen) => writeJson(
        req,
        200,
        info(
          latest: {
            'version': '0.1.132',
            'notes_url': '',
            'checked_at': '2026-10-11T10:00:00.000000000Z',
          },
        ),
      ),
    );
    addTearDown(server.stop);
    final c = VersionController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();

    // A release nothing can install is news, not a button.
    expect(updateTarget(c.value), isEmpty);
  });

  test('an update is asked for, then the state is read again', () async {
    final bodies = <String>[];
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        bodies.add(seen.body);
        writeJson(req, 202, {
          'id': 'r1',
          'action': 'apply',
          'target_version': '0.1.132',
          'ignore_maintenance_window': true,
          'state': 'pending',
          'message': '',
          'requested_by_email': 'owner@example.com',
          'requested_at': '2026-10-11T10:05:00.000000000Z',
          'picked_at': null,
          'finished_at': null,
        });
        return;
      }
      writeJson(req, 200, info(updater: updater(state: 'updating')));
    });
    addTearDown(server.stop);
    final c = VersionController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.applyNow('0.1.132', ignoreMaintenanceWindow: true);

    expect(jsonDecode(bodies.single), {
      'target_version': '0.1.132',
      'ignore_maintenance_window': true,
    });
    // The reply is the queued request, not the result: the screen reloads
    // the version info so the updater's state is what is shown.
    expect(c.value!.updater!.state, UpdaterStatusState.updating);
    expect(c.requestFailure, isNull);
  });

  test('a refused request is reported and the page stays readable', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        writeJson(req, 409, {
          'error': {'code': 'conflict', 'message': 'an update is running'},
        });
        return;
      }
      writeJson(req, 200, info(updater: updater()));
    });
    addTearDown(server.stop);
    final c = VersionController(
      OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
    );

    await c.refresh();
    await c.applyNow('0.1.132');

    expect(c.requestFailure!.kind, 'unexpected');
    expect(c.value, isNotNull);
  });
}
