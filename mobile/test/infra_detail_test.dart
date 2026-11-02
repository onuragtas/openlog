// The infrastructure and monitor screens: the web's sort and page defaults,
// the web's rules for a status, and what an older server's 404 looks like.
import 'dart:convert';
import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/detail.dart';
import 'package:openlog_mobile/src/infra_detail.dart';
import 'package:openlog_mobile/src/kubernetes.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/ui/failure_text.dart';
import 'package:openlog_mobile/src/ui/host_tabs.dart';
import 'package:openlog_mobile/src/ui/monitor_screens.dart';
import 'package:openlog_mobile/src/ui/rule_screen.dart';
import 'package:openlog_mobile/src/ui/workload_screen.dart';

import 'fake_server.dart';

const _t = '2026-10-28T09:00:00.000000000Z';

/// What the server's catch-all answers for an endpoint it has not got.
const _noEndpoint = {
  'error': {'code': 'not_found', 'message': 'no such endpoint'},
};

Map<String, Object?> _budget({double? remaining = 0.6, double? burn = 0.4}) => {
  'requests': 1000.0,
  'good': 990.0,
  'bad': 10.0,
  'sli': 0.99,
  'budget_requests': 100.0,
  'budget_consumed': 10.0,
  'budget_remaining': 90.0,
  'remaining_ratio': remaining,
  'burn_rate': burn,
  'met': true,
};

Map<String, Object?> _sloResults() => {
  'slo': {
    'id': 's1',
    'name': 'Checkout availability',
    'description': '',
    'service_name': 'checkout',
    'service_namespace': null,
    'environment': 'prod',
    'sli_type': 'availability',
    'latency_threshold_ms': null,
    'objective': 99.9,
    'window_days': 30,
    'created_by_email': '',
    'updated_by_email': '',
    'created_at': _t,
    'updated_at': _t,
  },
  'status': {'from': _t, 'to': _t, 'window_days': 30, 'budget': _budget()},
  'step': '1h',
  'burn': [
    {
      'name': 'fast',
      'factor': 14.4,
      'long_seconds': 3600,
      'short_seconds': 300,
      'long': _budget(),
      'short': _budget(),
      'rate': 0.4,
      'ratio': 0.03,
      'breaching': false,
    },
  ],
  'series': [
    {
      't': 1,
      'requests': 1.0,
      'good': 1.0,
      'bad': 0.0,
      'sli': 1.0,
      'burn_rate': 0.0,
      'remaining_ratio': 0.9,
    },
    {
      't': 2,
      'requests': 0.0,
      'good': 0.0,
      'bad': 0.0,
      'sli': null,
      'burn_rate': null,
      'remaining_ratio': null,
    },
  ],
};

Map<String, Object?> _monitor({
  bool enabled = true,
  bool late = false,
  String status = 'success',
}) => {
  'id': 'j1',
  'name': 'Nightly backup',
  'description': '',
  'kind': 'cron',
  'cron': '0 3 * * *',
  'time_zone': 'UTC',
  'interval_seconds': 0,
  'grace_seconds': 300,
  'enabled': enabled,
  'tags': <String>[],
  'ping_url': 'https://x/ping/abc',
  'created_by_email': '',
  'updated_by_email': '',
  'created_at': _t,
  'updated_at': _t,
  'state': {
    'status': status,
    'last_ping_at': _t,
    'last_started_at': null,
    'last_finished_at': _t,
    'last_duration_ms': 1500.0,
    'last_exit_code': 0,
    'last_message': 'done',
    'expected_at': _t,
    'consecutive_failures': 0,
    'late': late,
  },
};

Map<String, Object?> _finding(String host, {String fixedIn = '1.2'}) => {
  'host_id': host,
  'host_name': 'web-$host',
  'vuln_id': 'DSA-1',
  'cve': 'CVE-2026-1',
  'severity': 'high',
  'score': 7.5,
  'ecosystem': 'Debian:12',
  'package': 'openssl',
  'version': '1.1',
  'fixed_in': fixedIn,
  'summary': 'A flaw',
  'first_seen': _t,
  'last_seen': _t,
};

Map<String, Object?> _rule() => {
  'id': 'r1',
  'name': 'High CPU',
  'description': 'Too busy',
  'type': 'metric_threshold',
  'severity': 'critical',
  'enabled': true,
  'interval_seconds': 60,
  'for_seconds': 0,
  'recovery_for_seconds': 0,
  'condition': {'metric': 'system.cpu.utilization', 'threshold': 0.9},
  'channel_ids': <String>[],
  'renotify_interval_seconds': 0,
  'flapping': {
    'enabled': false,
    'transitions': 0,
    'window_seconds': 0,
    'hold_seconds': 0,
  },
  'runbook_url': '',
  'labels': <String, String>{},
  'version': 1,
  'created_by_user_id': null,
  'created_by_email': '',
  'created_at': _t,
  'updated_at': _t,
  'status': {
    'state': 'firing',
    'series_pending': 0,
    'series_firing': 1,
    'open_incidents': 1,
    'last_evaluated_at': _t,
    'last_result': '',
    'last_error': '',
    'last_duration_ms': 3,
    'next_evaluation_at': null,
    'owner': null,
  },
  'series': [
    {
      'series_key': 'k1',
      'labels': {'host.name': 'web-1'},
      'state': 'firing',
      'value': 0.95,
      'pending_since': null,
      'firing_since': _t,
      'recovering_since': null,
      'incident_id': null,
      'flapping': false,
      'updated_at': _t,
    },
  ],
};

Map<String, Object?> _evaluations() => {
  'from': _t,
  'to': _t,
  'step_seconds': 60,
  'evaluations': [
    {
      'at': _t,
      'firing_series': 1.0,
      'evaluations': 3,
      'errors': 1,
      'duration_ms': 4,
      'max_duration_ms': 9,
    },
    {
      'at': _t,
      'firing_series': null,
      'evaluations': 2,
      'errors': 0,
      'duration_ms': 7,
      'max_duration_ms': 7,
    },
  ],
  'series': [
    {
      'series_key': 'k1',
      'labels': {'host.name': 'web-1'},
      'points': [
        [1, 0.5, 'ok'],
        [2, null, 'ok'],
        [3, 0.9, 'firing'],
      ],
    },
  ],
  'truncated': false,
};

Map<String, Object?> _workload() => {
  'cluster_uid': 'c1',
  'cluster_name': 'prod',
  'namespace': 'shop',
  'kind': 'Job',
  'name': 'migrate',
  'uid': 'u1',
  'desired': 3,
  'ready': 2,
  'available': 5,
  'updated': 1,
  'health': 'degraded',
  'pods': 0,
  'restarts': 4,
  'cpu_usage': null,
  'memory_working_set': null,
  'cpu_sparkline': <Object>[],
  'memory_sparkline': <Object>[],
  'created_at': null,
  'first_seen': _t,
  'last_seen': _t,
  'reporting': true,
  'pod_list': <Object>[],
  'hpa': {
    'name': 'migrate-hpa',
    'min_replicas': 1.0,
    'max_replicas': null,
    'current_replicas': 2.0,
    'desired_replicas': 3.0,
  },
  'attributes': <String, String>{},
};

OpenlogClient _client(FakeServer s) =>
    OpenlogClient(baseUrl: s.baseUrl)..token = 'olm_x';

Future<void> _pump(WidgetTester tester, Widget home) async {
  // A phone, so a row that does not fit fails here rather than in a pocket.
  tester.view.physicalSize = const Size(390, 844);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.reset);
  await tester.pumpWidget(
    MaterialApp(
      localizationsDelegates: L.localizationsDelegates,
      supportedLocales: L.supportedLocales,
      locale: const Locale('en'),
      home: home,
    ),
  );
}

/// A client whose answers come from [answer]: a real server cannot answer
/// inside a widget test's fake clock. Null is what an older server says.
OpenlogClient _mocked(Object? Function(String path) answer) => OpenlogClient(
  baseUrl: 'http://x',
  httpClient: MockClient((request) async {
    final body = answer(request.url.path);
    return http.Response(
      jsonEncode(body ?? _noEndpoint),
      body == null ? 404 : 200,
      headers: {'content-type': 'application/json; charset=utf-8'},
    );
  }),
);

Future<void> _settle(WidgetTester tester) async {
  for (var i = 0; i < 6; i++) {
    await tester.pump(const Duration(milliseconds: 50));
  }
}

void main() {
  // testWidgets swaps in a binding that answers every request with a 400, and
  // these talk to a real loopback server.
  setUpAll(() => HttpOverrides.global = null);

  group('an endpoint or a record', () {
    test('the catch-all and a proxy are "no such endpoint"', () {
      expect(
        ApiException(
          404,
          code: 'not_found',
          message: 'no such endpoint',
        ).noSuchEndpoint,
        isTrue,
      );
      expect(ApiException(404).noSuchEndpoint, isTrue);
      expect(ApiException(405).noSuchEndpoint, isTrue);
    });

    test('a record that is gone says so, and a 500 is not older', () {
      expect(
        ApiException(
          404,
          code: 'not_found',
          message: 'slo not found',
        ).noSuchEndpoint,
        isFalse,
      );
      expect(ApiException(500).noSuchEndpoint, isFalse);
    });
  });

  group('sort and paging', () {
    Future<(FakeServer, OpenlogClient)> serve(Map<String, Object?> body) async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, body),
      );
      addTearDown(server.stop);
      return (server, _client(server));
    }

    test('every list asks with the web defaults', () async {
      final (s, c) = await serve({
        'hosts': <Object>[],
        'containers': <Object>[],
        'pods': <Object>[],
        'nodes': <Object>[],
        'workloads': <Object>[],
        'services': <Object>[],
        'vulnerabilities': <Object>[],
        'severity_counts': <String, int>{},
        'projects': <Object>[],
        'total': 0,
        'step': '60s',
      });

      await HostsController(c).refresh();
      expect(s.requests.last.query, 'sort=name&dir=asc&limit=50');
      await ContainersController(c).refresh();
      expect(s.requests.last.query, 'sort=name&dir=asc&limit=50');
      await PodsController(c).refresh();
      expect(s.requests.last.query, 'sort=name&dir=asc&limit=50');
      await KubernetesNodesController(c).refresh();
      expect(s.requests.last.query, 'sort=name&dir=asc&limit=50');
      await KubernetesWorkloadsController(c).refresh();
      expect(s.requests.last.query, 'sort=name&dir=asc&limit=50');
      await ProfilesController(c).refresh();
      expect(s.requests.last.query, 'sort=samples&dir=desc&limit=50');

      await VulnerabilitiesController(c).refresh();
      expect(
        s.requests.lastWhere((r) => r.path == '/api/v1/vulnerabilities').query,
        'sort=severity&dir=asc&limit=50',
      );
    });

    test('a next page is added to the rows, at their offset', () async {
      final answers = [
        {
          'hosts': [_host('a')],
          'has_more': true,
        },
        {
          'hosts': [_host('b')],
          'has_more': false,
        },
      ];
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, answers.removeAt(0)),
      );
      addTearDown(server.stop);
      final c = HostsController(_client(server));

      await c.refresh();
      expect(c.hasMore, isTrue);
      await c.loadMore();

      expect(server.requests.last.query, contains('offset=1'));
      expect(c.items.map((h) => h.hostId), ['a', 'b']);
      expect(c.hasMore, isFalse);
    });

    test('an older server sends no has_more, so no next page', () async {
      final (_, c) = await serve({
        'hosts': [_host('a')],
      });
      final hosts = HostsController(c);

      await hosts.refresh();

      expect(hosts.items, hasLength(1));
      expect(hosts.hasMore, isFalse);
    });

    test('a column starts the way the web starts it', () async {
      final (s, c) = await serve({'hosts': <Object>[]});
      final hosts = HostsController(c);

      await hosts.setSort('cpu');
      expect(s.requests.last.query, contains('sort=cpu&dir=desc'));
      await hosts.setSort('os');
      expect(s.requests.last.query, contains('sort=os&dir=asc'));
    });

    test('the grouped containers are not cut into a page', () async {
      final (s, c) = await serve({
        'containers': <Object>[],
        'projects': <Object>[],
        'total': 0,
        'step': '60s',
      });

      await (ContainersController(c)..grouped = true).refresh();

      expect(s.requests.last.query, isNot(contains('limit=')));
    });

    test('a search of profiles reads past one page', () async {
      final (s, c) = await serve({'services': <Object>[], 'has_more': true});
      final profiles = ProfilesController(c)..query = 'checkout';

      await profiles.refresh();

      expect(s.requests.last.query, contains('limit=1000'));
      expect(profiles.hasMore, isFalse);
    });

    test('the severity filters the list, and the counts come back', () async {
      final (s, c) = await serve({
        'vulnerabilities': <Object>[],
        'severity_counts': {'high': 3},
      });
      final v = VulnerabilitiesController(c)..severity = 'high';

      await v.refresh();

      expect(
        s.requests.firstWhere((r) => r.path == '/api/v1/vulnerabilities').query,
        contains('severity=high'),
      );
      expect(v.severityCounts['high'], 3);
    });

    test('costs: a server without /costs/summary still has a bill', () async {
      final server = await FakeServer.start((req, seen) {
        switch (seen.path) {
          case '/api/v1/costs/summary':
            writeJson(req, 404, _noEndpoint);
          case '/api/v1/costs/hosts':
            writeJson(req, 200, {
              'hosts': <Object>[],
              'total': 0,
              'summary': _costSummary(),
              'pricing': _pricing(),
            });
          case '/api/v1/costs/services':
            writeJson(req, 200, {
              'services': <Object>[],
              'total': 0,
              'summary': _costSummary(),
              'pricing': _pricing(),
            });
          default:
            writeJson(req, 200, {
              'step': '1h',
              'points': <Object>[],
              'pricing': _pricing(),
              'from': 1,
              'to': 2,
            });
        }
      });
      addTearDown(server.stop);
      final c = CostsController(_client(server));

      await c.refresh();

      expect(c.failure, isNull);
      expect(c.fleet, isNull);
      expect(c.summary!.total, 12.5);
    });
  });

  group('the web\'s rules', () {
    test('a budget reads by what is left', () {
      SloBudget b(double? r) => SloBudget.fromJson(_budget(remaining: r));
      expect(budgetLevel(b(0.26)), BudgetLevel.healthy);
      expect(budgetLevel(b(0.25)), BudgetLevel.warning);
      expect(budgetLevel(b(0.01)), BudgetLevel.warning);
      expect(budgetLevel(b(0)), BudgetLevel.exhausted);
      expect(budgetLevel(b(-0.5)), BudgetLevel.exhausted);
      expect(budgetLevel(b(null)), BudgetLevel.none);
      expect(budgetLevel(null), BudgetLevel.none);
    });

    test('numbers are written as the web writes them', () {
      expect(formatRatio(0.99912, digits: 3), '99.912%');
      expect(formatRatio(null), '–');
      expect(formatBurnRate(14.4), '14.4×');
      expect(formatBurnRate(1.256), '1.26×');
      expect(formatBurnRate(null), '–');
      expect(formatObjective(99.9), '99.9%');
      expect(
        sloTarget(
          serviceName: 'checkout',
          serviceNamespace: 'shop',
          environment: 'prod',
        ),
        'checkout · shop (prod)',
      );
    });

    test('the burndown has no holes and stops below exhausted', () {
      SloPoint p(double? r) => SloPoint.fromJson({
        't': 1,
        'requests': 0.0,
        'good': 0.0,
        'bad': 0.0,
        'sli': null,
        'burn_rate': null,
        'remaining_ratio': r,
      });
      expect(burndownValues([p(0.5), p(null), p(-2)]), [0.5, 0.5, -0.25]);
      expect(burndownValues([p(null)]), isEmpty);
    });

    test('a monitor reads paused, then late, then by its last run', () {
      JobState s({bool enabled = true, bool late = false, String st = ''}) =>
          jobState(
            JobMonitor.fromJson(
              _monitor(enabled: enabled, late: late, status: st),
            ),
          );
      expect(s(enabled: false, late: true, st: 'failure'), JobState.paused);
      expect(s(late: true, st: 'success'), JobState.late);
      expect(s(st: 'success'), JobState.ok);
      expect(s(st: 'failure'), JobState.failing);
      expect(s(st: 'missed'), JobState.failing);
      expect(s(st: 'overrun'), JobState.failing);
      expect(s(st: 'running'), JobState.running);
      expect(s(st: 'what'), JobState.unknown);
    });

    test('durations: zero means openlog did not see both ends', () {
      expect(formatJobDuration(0), '–');
      expect(formatJobDuration(null), '–');
      expect(formatJobDuration(250), '250 ms');
      expect(formatJobDuration(1500), '1.5 s');
      expect(formatJobDuration(125000), '2m 5s');
      expect(formatLate(120), '+2m');
      expect(formatLate(-30), '−30s');
      expect(formatLate(0), '0s');
      expect(formatSeconds(86400), '1d');
      expect(formatSeconds(0), '–');
    });

    test('a score of zero is no score, and a fix may be unknown', () {
      expect(formatScore(0), '–');
      expect(formatScore(9.81), '9.8');
      final f = VulnFinding.fromJson(_finding('a', fixedIn: ''));
      expect(upgradeText(f), '1.1');
      expect(upgradeText(VulnFinding.fromJson(_finding('a'))), '1.1 → 1.2');
      expect(advisoryLabel('CVE-1', 'CVE-1'), 'CVE-1');
      expect(advisoryLabel('DSA-1', 'CVE-1'), 'DSA-1 · CVE-1');
    });

    test('money keeps its cents below a hundred', () {
      expect(formatMoney(1.72, 'USD'), '1.72 USD');
      expect(formatMoney(1204.4, 'EUR'), '1204 EUR');
      expect(formatMoney(5, 'nonsense'), '5.00 USD');
    });

    test('inventory: the order, the filter, the summary, a port', () {
      InventoryItem i(String c, String k, [Object? d]) =>
          InventoryItem(category: c, key: k, data: d);
      final items = [
        i('zeta', 'z'),
        i('package', 'openssl', {'version': '3.0', 'x': 1}),
        i('os', 'linux'),
        i('package', 'curl'),
      ];
      expect(countByCategory(items).map((c) => c.category), [
        'os',
        'package',
        'zeta',
      ]);
      expect(countByCategory(items)[1].count, 2);
      expect(filterInventory(items, 'package', ''), hasLength(2));
      // Every word has to occur, in the key or in the body.
      expect(filterInventory(items, '', 'openssl 3.0'), hasLength(1));
      expect(filterInventory(items, '', 'openssl 4.0'), isEmpty);
      expect(summarizeInventory({'version': '3.0', 'x': 1}), 'version=3.0');
      expect(summarizeInventory(null), '');
      expect(
        inventoryKey(i('listening_port', 'tcp:0.0.0.0:80')),
        'tcp 0.0.0.0:80',
      );
      expect(inventoryKey(i('listening_port', 'tcp:[::]:80')), 'tcp [::]:80');
    });

    test('an evaluation history adds up, and a null is no point', () {
      final h = AlertRuleEvaluations.fromJson(_evaluations());
      final t = evaluationTotals(h);
      expect(t.evaluations, 5);
      expect(t.errors, 1);
      expect(t.lastDurationMs, 7);
      expect(evaluationValues(h.series.first), [0.5, 0.9]);
      expect(labelsText({'host.name': 'web-1', 'a': 'b'}), 'web-1');
      expect(labelsText({'a': 'b', 'host.id': 'x'}), 'a=b');
    });
  });

  group('an older server', () {
    Future<OpenlogClient> old() async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 404, _noEndpoint),
      );
      addTearDown(server.stop);
      return _client(server);
    }

    test('every new detail is a plain state, not a failure', () async {
      final c = await old();
      final controllers = <DetailController<Object?>>[
        SloDetailController(c, 's1'),
        JobDetailController(c, 'j1'),
        HostInventoryController(c, 'h1'),
        HostMetricsController(c, 'h1'),
        CostHostController(c, 'h1'),
        AlertRuleController(c, 'r1'),
        RuleEvaluationsController(c, 'r1'),
        KubernetesWorkloadController(
          c,
          clusterUid: 'c1',
          namespace: 'n',
          kind: 'Job',
          name: 'x',
        ),
      ];
      for (final x in controllers) {
        await x.refresh();
        expect(x.notOffered, isTrue, reason: '${x.runtimeType}');
        expect(x.failure, isNull, reason: '${x.runtimeType}');
      }
    });

    test('the findings lists are too', () async {
      final c = await old();
      for (final x in <FindingsController>[
        VulnerabilityController(c, 'DSA-1'),
        HostVulnerabilitiesController(c, 'h1'),
      ]) {
        await x.refresh();
        expect(x.notOffered, isTrue);
        expect(x.failure, isNull);
        expect(x.items, isEmpty);
      }
    });

    test('a record that is gone is still gone', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 404, {
          'error': {'code': 'not_found', 'message': 'slo not found'},
        }),
      );
      addTearDown(server.stop);
      final c = SloDetailController(_client(server), 's1');

      await c.refresh();

      expect(c.notOffered, isFalse);
      expect(c.failure?.kind, 'detailGone');
    });

    test('a container still opens, and says so about its services', () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.path.endsWith('/services')) {
          writeJson(req, 404, _noEndpoint);
        } else if (seen.path.endsWith('/timeseries')) {
          writeJson(req, 500, {
            'error': {'code': 'internal', 'message': 'not this one'},
          });
        } else {
          writeJson(req, 200, _container());
        }
      });
      addTearDown(server.stop);
      final c = ContainerController(_client(server), 'c1');

      await c.refresh();

      expect(c.failure, isNull);
      expect(c.value!.name, 'checkout-1');
      expect(c.servicesNotOffered, isTrue);
      expect(c.servicesError, isNull);
    });

    test('a host keeps its screen with no cost answer', () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.path == '/api/v1/hosts/h1') {
          writeJson(req, 200, _host('h1'));
        } else if (seen.path.endsWith('/services')) {
          writeJson(req, 200, {
            'snapshot_id': '',
            'snapshot_time': null,
            'items': <Object>[],
          });
        } else {
          writeJson(req, 404, _noEndpoint);
        }
      });
      addTearDown(server.stop);
      final c = HostController(_client(server), 'h1');

      await c.refresh();

      expect(c.failure, isNull);
      expect(c.cost, isNull);
    });

    testWidgets('the screen says it plainly, with no banner', (tester) async {
      final sections = Sections(client: _mocked((_) => null));
      addTearDown(sections.dispose);
      await _pump(
        tester,
        SloScreen(
          session: SessionController(store: MemoryTokenStore()),
          sections: sections,
          id: 's1',
          name: 'Checkout',
        ),
      );
      await _settle(tester);

      expect(find.byKey(const Key('not-offered')), findsOneWidget);
      expect(find.text('That is no longer on the server.'), findsNothing);
      // An empty banner lays out to nothing, so a list may not build it at all.
      for (final b in tester.widgetList<FailureBanner>(
        find.byType(FailureBanner),
      )) {
        expect(b.failure, isNull);
      }
    });
  });

  group('the detail calls', () {
    test('a container runs its busiest service first', () async {
      final server = await FakeServer.start((req, seen) {
        if (seen.path.endsWith('/services')) {
          Map<String, Object?> svc(String name, double rpm) => {
            'requests': 1.0,
            'throughput': rpm,
            'errors': 0.0,
            'error_rate': 0.0,
            'avg_ms': null,
            'p50_ms': null,
            'p95_ms': null,
            'p99_ms': null,
            'apdex': null,
            'service_name': name,
            'service_namespace': '',
            'environment': '',
            'first_seen': _t,
            'last_seen': _t,
            'apdex_t_ms': 500.0,
          };
          writeJson(req, 200, {
            'services': [svc('quiet', 1), svc('busy', 90)],
          });
        } else if (seen.path.endsWith('/timeseries')) {
          writeJson(req, 500, {
            'error': {'code': 'internal', 'message': 'not this one'},
          });
        } else {
          writeJson(req, 200, _container());
        }
      });
      addTearDown(server.stop);
      final c = ContainerController(_client(server), 'c1');

      await c.refresh();

      expect(c.services.map((x) => x.serviceName), ['busy', 'quiet']);
    });

    test('the rule switch goes to the two endpoints the web uses', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, <String, Object>{}),
      );
      addTearDown(server.stop);
      final c = _client(server);

      await c.setAlertRuleEnabled('r1', enabled: true);
      await c.setAlertRuleEnabled('r1', enabled: false);

      expect(server.requests.map((r) => r.path), [
        '/api/v1/alerts/rules/r1/enable',
        '/api/v1/alerts/rules/r1/disable',
      ]);
    });

    test('an evaluation window is the range picked, a day at first', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, _evaluations()),
      );
      addTearDown(server.stop);
      final c = RuleEvaluationsController(_client(server), 'r1');

      await c.refresh();
      expect(c.hours, 24);
      final q = Uri.splitQueryString(server.requests.last.query);
      final span = int.parse(q['to']!) - int.parse(q['from']!);
      expect(span, const Duration(hours: 24).inMilliseconds);
    });

    test('host metrics: the busy line leaves idle out', () async {
      final server = await FakeServer.start((req, seen) {
        final name = Uri.splitQueryString(seen.query)['name'];
        writeJson(req, 200, {
          'metric': {
            'name': name,
            'type': 'gauge',
            'unit': '',
            'description': '',
          },
          'step': '60s',
          'series': name == 'system.cpu.utilization'
              ? [
                  {
                    'attributes': {'cpu.mode': 'user'},
                    'points': [
                      [1, 0.2],
                      [2, 0.3],
                    ],
                  },
                  {
                    'attributes': {'cpu.mode': 'system'},
                    'points': [
                      [1, 0.1],
                      [2, 0.1],
                    ],
                  },
                  {
                    'attributes': {'cpu.mode': 'idle'},
                    'points': [
                      [1, 0.7],
                      [2, 0.6],
                    ],
                  },
                ]
              : <Object>[],
        });
      });
      addTearDown(server.stop);
      final c = HostMetricsController(_client(server), 'h1');

      await c.refresh();

      final cpu = c.value!.firstWhere((x) => x.id == 'cpu');
      expect(cpu.lines.first.label, isEmpty);
      expect(cpu.lines.first.values.first, closeTo(0.3, 1e-9));
      expect(cpu.lines.first.values.last, closeTo(0.4, 1e-9));
      expect(cpu.lines.map((l) => l.label), ['', 'user', 'system']);
      expect(c.value!.map((x) => x.id), [
        'cpu',
        'load',
        'memory',
        'filesystem',
        'disk',
        'network',
      ]);
    });

    test('one chart failing leaves the others', () async {
      final server = await FakeServer.start((req, seen) {
        final name = Uri.splitQueryString(seen.query)['name'];
        if (name == 'system.disk.io') {
          writeJson(req, 500, {
            'error': {'code': 'internal', 'message': 'boom'},
          });
          return;
        }
        writeJson(req, 200, {
          'metric': {
            'name': name,
            'type': 'gauge',
            'unit': '',
            'description': '',
          },
          'step': '60s',
          'series': <Object>[],
        });
      });
      addTearDown(server.stop);
      final c = HostMetricsController(_client(server), 'h1');

      await c.refresh();

      expect(c.failure, isNull);
      expect(c.value!.firstWhere((x) => x.id == 'disk').error, 'boom');
      expect(c.value!.firstWhere((x) => x.id == 'cpu').error, isNull);
    });

    test('an advisory falls back on its rows on an older server', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, {
          'vuln_id': 'DSA-1',
          'findings': [_finding('a', fixedIn: ''), _finding('b')],
        }),
      );
      addTearDown(server.stop);
      final c = VulnerabilityController(_client(server), 'DSA-1');

      await c.refresh();

      expect(c.advisory, isNull);
      expect(c.severity, VulnSeverity.high);
      expect(c.withoutFix, 1);
      expect(c.hasMore, isFalse);
    });

    test('an advisory\'s own summary beats the page', () async {
      final server = await FakeServer.start(
        (req, _) => writeJson(req, 200, {
          'vuln_id': 'DSA-1',
          'findings': <Object>[],
          'advisory': {
            'cve': 'CVE-9',
            'severity': 'critical',
            'score': 9.8,
            'summary': 'Bad',
            'findings': 40,
            'without_fix': 12,
          },
        }),
      );
      addTearDown(server.stop);
      final c = VulnerabilityController(_client(server), 'DSA-1');

      await c.refresh();

      expect(c.severity, VulnSeverity.critical);
      expect(c.withoutFix, 12);
    });
  });

  group('on a phone', () {
    Sections serve(Object? Function(String path) answer) {
      final sections = Sections(client: _mocked(answer));
      addTearDown(sections.dispose);
      return sections;
    }

    final session = SessionController(store: MemoryTokenStore());

    testWidgets('an SLO reads its status, target and windows', (tester) async {
      final s = serve((_) => _sloResults());
      await _pump(
        tester,
        SloScreen(session: session, sections: s, id: 's1', name: 'x'),
      );
      await _settle(tester);

      expect(find.text('Healthy'), findsOneWidget);
      expect(find.text('checkout (prod)'), findsOneWidget);
      expect(find.text('Fast (1 h / 5 m)'), findsOneWidget);
      expect(find.byKey(const Key('slo-burndown')), findsOneWidget);
    });

    testWidgets('a job monitor reads its state and its runs', (tester) async {
      final s = serve(
        (_) => {
          'monitor': _monitor(late: true),
          'runs': [
            {
              'timestamp': _t,
              'status': 'overrun',
              'started_at': null,
              'duration_ms': 0.0,
              'exit_code': 0,
              'late_seconds': 120.0,
              'message': '',
              'source': '',
            },
          ],
          'summary': {
            'runs': 1,
            'failures': 0,
            'missed': 0,
            'avg_ms': null,
            'max_ms': null,
            'last_at': null,
          },
        },
      );
      await _pump(
        tester,
        JobScreen(session: session, sections: s, id: 'j1', name: 'x'),
      );
      await _settle(tester);

      expect(find.text('Late'), findsOneWidget);
      expect(find.text('Overrun'), findsOneWidget);
      expect(find.textContaining('Exit –'), findsOneWidget);
      expect(find.textContaining('+2m'), findsOneWidget);
    });

    testWidgets('an advisory says when upgrading will not help', (
      tester,
    ) async {
      final s = serve(
        (_) => {
          'vuln_id': 'DSA-1',
          'findings': [_finding('a', fixedIn: '')],
        },
      );
      await _pump(
        tester,
        VulnerabilityScreen(session: session, sections: s, id: 'DSA-1'),
      );
      await _settle(tester);

      expect(find.byKey(const Key('vuln-no-fix')), findsOneWidget);
      expect(find.text('web-a'), findsOneWidget);
    });

    testWidgets('a workload reads a Job as a Job, and a blank as a blank', (
      tester,
    ) async {
      final s = serve((path) {
        if (path.endsWith('/timeseries')) return null;
        if (path.endsWith('/events')) return {'events': <Object>[]};
        return _workload();
      });
      await _pump(
        tester,
        WorkloadScreen(
          session: session,
          sections: s,
          clusterUid: 'c1',
          namespace: 'shop',
          kind: 'Job',
          name: 'migrate',
        ),
      );
      await _settle(tester);

      expect(find.text('Succeeded'), findsOneWidget);
      expect(find.text('Failed'), findsOneWidget);
      expect(find.text('2 / 3'), findsOneWidget);
      expect(find.text('migrate-hpa: 2 → 3 (min 1, max –)'), findsOneWidget);
    });

    testWidgets('a rule shows its series and its history', (tester) async {
      final s = serve(
        (path) => path.endsWith('/evaluations') ? _evaluations() : _rule(),
      );
      await _pump(
        tester,
        RuleScreen(session: session, sections: s, id: 'r1', name: 'x'),
      );
      await _settle(tester);
      await tester.scrollUntilVisible(
        find.byKey(const Key('rule-history-line')),
        200,
        scrollable: find.byType(Scrollable).first,
      );

      expect(
        find.text('5 evaluations, 1 errors; the last one took 7 ms.'),
        findsOneWidget,
      );
      expect(find.byKey(const Key('rule-series-k1')), findsOneWidget);
    });

    testWidgets('a rule on a server without history still reads', (
      tester,
    ) async {
      final s = serve((path) => path.endsWith('/evaluations') ? null : _rule());
      await _pump(
        tester,
        RuleScreen(session: session, sections: s, id: 'r1', name: 'x'),
      );
      await _settle(tester);
      await tester.scrollUntilVisible(
        find.byKey(const Key('not-offered')),
        200,
        scrollable: find.byType(Scrollable).first,
      );

      expect(find.text('High CPU'), findsOneWidget);
    });

    testWidgets('a host\'s inventory filters as it is typed', (tester) async {
      final s = serve(
        (_) => {
          'snapshot_id': 's1',
          'snapshot_time': _t,
          'items': [
            {
              'category': 'package',
              'key': 'openssl',
              'data': {'version': '3.0'},
            },
            {'category': 'os', 'key': 'linux', 'data': null},
          ],
        },
      );
      await _pump(
        tester,
        HostInventoryScreen(
          session: session,
          sections: s,
          hostId: 'h1',
          hostName: 'web-1',
        ),
      );
      await _settle(tester);

      expect(find.text('2 of 2'), findsOneWidget);
      await tester.enterText(
        find.byKey(const Key('host-inventory-search')),
        'openssl',
      );
      await tester.pump();
      expect(find.text('1 of 2'), findsOneWidget);
      expect(find.byKey(const Key('host-inv-os-linux')), findsNothing);
    });

    testWidgets('a host that never sent a snapshot says that', (tester) async {
      final s = serve(
        (_) => {'snapshot_id': '', 'snapshot_time': null, 'items': <Object>[]},
      );
      await _pump(
        tester,
        HostInventoryScreen(
          session: session,
          sections: s,
          hostId: 'h1',
          hostName: 'web-1',
        ),
      );
      await _settle(tester);

      expect(
        find.text('This host has not sent a complete inventory snapshot yet.'),
        findsOneWidget,
      );
    });
  });
}

Map<String, Object?> _host(String id) => {
  'host_id': id,
  'host_name': id,
  'os_description': '',
  'arch': '',
  'agent_version': '',
  'last_seen': _t,
  'resource_attributes': <String, String>{},
};

Map<String, Object?> _costSummary() => {
  'currency': 'USD',
  'total': 12.5,
  'services': 1.0,
  'unallocated': 1.0,
  'unattributed': 1.0,
  'idle': 1.0,
  'idle_share': 0.1,
  'per_hour': 1.0,
  'hosts': 1,
  'priced_hosts': 1,
  'unpriced_hosts': 0,
  'host_hours': 1.0,
};

Map<String, Object?> _pricing() => {
  'version': 1,
  'updated': '2026-01-01',
  'currency': 'USD',
  'note': '',
  'estimated': true,
};

Map<String, Object?> _container() => {
  'container_id': 'c1',
  'name': 'checkout-1',
  'image_name': 'ghcr.io/resoft/checkout',
  'image_tags': ['v3'],
  'runtime': 'docker',
  'host_id': 'h1',
  'host_name': 'web-1',
  'compose_project': '',
  'compose_service': '',
  'k8s_pod_name': '',
  'k8s_namespace_name': '',
  'k8s_container_name': '',
  'state': 'running',
  'health': 'healthy',
  'started_at': null,
  'restart_count': 0,
  'first_seen': _t,
  'last_seen': _t,
  'reporting': true,
  'cpu_utilization': 0.31,
  'memory_usage': 125829120,
  'memory_limit': 0,
  'cpu_sparkline': <Object>[],
  'memory_sparkline': <Object>[],
  'attributes': <String, String>{},
};
