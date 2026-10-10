// The fleet: which versions can be rolled back to, and what a policy
// change sends.
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/sections.dart';

import 'fake_server.dart';

Map<String, Object?> policy({String mode = 'notify'}) => {
  'mode': mode,
  'channel': 'stable',
  'target': 'latest',
  'pinned_version': null,
  'waves': [10, 50, 100],
  'wave_soak_minutes': 30,
  'halt_failure_rate': 0.1,
  'maintenance_windows': [
    {
      'days': ['sat', 'sun'],
      'start': '02:00',
      'end': '05:00',
    },
  ],
  'php_agent': {
    'mode': 'off',
    'version': '',
    'reload': 'none',
    'exclude_bins': <String>[],
    'changed_at': null,
  },
  'java_agent': {'mode': 'off', 'version': '', 'changed_at': null},
  'is_default': false,
  'updated_at': null,
  'updated_by_email': 'owner@example.com',
};

Map<String, Object?> summary() => {
  'total_hosts': 3,
  'active_hosts': 3,
  'update_capable': 3,
  'not_update_capable': <Object>[],
  'outdated': 1,
  'unsupported': 0,
  'in_progress': 0,
  'failed': 0,
  'held': 0,
  'pinned': 0,
  'versions': <Object>[],
  'latest': {'stable': null, 'beta': null},
  'target': null,
  'oldest_supported_version': '0.1.100',
  'policy_mode': 'notify',
  'update_available': false,
  'stale_after_seconds': 900,
  'catalog': {
    'status': 'ok',
    'source': 'github',
    'checked_at': '2026-10-08T09:00:00.000000000Z',
    'last_success_at': '2026-10-08T09:00:00.000000000Z',
    'error': '',
    'releases': 40,
    'warnings': <String>[],
  },
  'current_rollout': null,
};

void main() {
  group('what the fleet can be rolled back to', () {
    test('the running versions below the target, newest first', () {
      expect(
        rollbackCandidates([
          '0.1.110',
          '0.1.113',
          '0.1.109',
          '0.1.113',
        ], '0.1.113'),
        ['0.1.110', '0.1.109'],
      );
    });

    test('without a target, everything it runs is a candidate', () {
      expect(rollbackCandidates(['0.2.0', '0.1.9'], null), ['0.2.0', '0.1.9']);
    });

    test('what is not a version is not a candidate', () {
      expect(rollbackCandidates(['dev', '', 'v0.1.9'], null), ['v0.1.9']);
    });
  });

  test(
    'changing the mode sends the rest of the policy back as it was',
    () async {
      final bodies = <String>[];
      // The fake keeps what it was told, as a server would: the screen
      // reloads after a write and has to see the new mode.
      var mode = 'notify';
      final server = await FakeServer.start((req, seen) {
        if (seen.method == 'PUT') {
          bodies.add(seen.body);
          mode = 'off';
          writeJson(req, 200, policy(mode: mode));
          return;
        }
        if (seen.path.endsWith('/policy')) {
          writeJson(req, 200, policy(mode: mode));
        } else if (seen.path.endsWith('/rollouts')) {
          writeJson(req, 200, {'rollouts': <Object>[]});
        } else if (seen.path.endsWith('/summary')) {
          writeJson(req, 200, summary());
        } else {
          writeJson(req, 200, {'hosts': <Object>[], 'next_cursor': null});
        }
      });
      addTearDown(server.stop);
      final c = FleetController(
        OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x',
      );

      await c.refresh();
      expect(await c.setMode('off'), isTrue);

      // Everything the contract requires goes back unchanged: a PUT that
      // left the waves out would reset them to the default.
      expect(bodies.single, contains('"mode":"off"'));
      expect(bodies.single, contains('"waves":[10,50,100]'));
      expect(bodies.single, contains('"wave_soak_minutes":30'));
      expect(bodies.single, contains('"halt_failure_rate":0.1'));
      expect(bodies.single, contains('"maintenance_windows":[{"days"'));
      // And the screen is reloaded, so the fleet state matches the policy.
      expect(c.policy!.mode, FleetMode.off);
    },
  );

  test('a viewer who tries anyway is told it needs an admin', () async {
    final server = await FakeServer.start((req, seen) {
      if (seen.method == 'POST') {
        writeJson(req, 403, {
          'error': {'code': 'permission_denied', 'message': 'admin required'},
        });
        return;
      }
      if (seen.path.endsWith('/policy')) {
        writeJson(req, 200, policy());
      } else if (seen.path.endsWith('/rollouts')) {
        writeJson(req, 200, {'rollouts': <Object>[]});
      } else if (seen.path.endsWith('/summary')) {
        writeJson(req, 200, summary());
      } else {
        writeJson(req, 200, {'hosts': <Object>[], 'next_cursor': null});
      }
    });
    addTearDown(server.stop);
    final client = OpenlogClient(baseUrl: server.baseUrl)..token = 'olm_x';
    final c = FleetController(client);

    final ok = await c.act(() => client.pauseRollout('r1'));

    expect(ok, isFalse);
    expect(c.failure?.kind, 'fleetForbidden');
  });
}
