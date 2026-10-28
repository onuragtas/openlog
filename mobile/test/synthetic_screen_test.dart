// The synthetic check screen: what a red check is opened for, and the
// shortcut that turns it into an alert.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/synthetics.dart';
import 'package:openlog_mobile/src/templates.dart';
import 'package:openlog_mobile/src/ui/synthetic_screen.dart';

final _client = OpenlogClient(baseUrl: 'http://127.0.0.1:1');

const _checkId = 'chk-1';

Map<String, Object?> _check() => {
  'id': _checkId,
  'name': 'Checkout health',
  'type': 'http',
  'enabled': true,
  'url': 'https://shop.example.com/health',
  'method': 'GET',
  'headers': <String, String>{},
  'body': '',
  'expected_status': [200],
  'assertion_type': 'none',
  'assertion_path': '',
  'assertion_value': '',
  'timeout_ms': 3000,
  'interval_seconds': 5,
  'locations': ['local'],
  'target': '',
  'dns_record_type': 'A',
  'dns_expected': <String>[],
  'tls_warning_days': 14,
  'created_by_email': '',
  'updated_by_email': '',
  'created_at': '2026-10-01T08:00:00.000000000Z',
  'updated_at': '2026-10-01T08:00:00.000000000Z',
  'status': [
    {
      'location': 'local',
      'next_run_at': '2026-10-28T09:00:05.000000000Z',
      'last_run_at': '2026-10-28T09:00:00.000000000Z',
      'last_success': false,
      'last_status_code': 503,
      'last_duration_ms': 240.0,
      'last_error_kind': 'status_code',
      'last_error': 'HTTP 503, expected 200',
    },
  ],
};

Map<String, Object?> _results() => {
  'check': _check(),
  'summary': {
    'from': '2026-10-27T09:00:00.000000000Z',
    'to': '2026-10-28T09:00:00.000000000Z',
    'step': '3600s',
    'runs': 288,
    'failures': 12,
    'uptime': 0.958,
    'avg_ms': 180.0,
    'p50_ms': 160.0,
    'p95_ms': 420.0,
    'p99_ms': 980.0,
    // Bucket starts are unix milliseconds, which is what the contract sends.
    'points': [
      {
        't': 1761634800000,
        'runs': 12,
        'failures': 0,
        'uptime': 1.0,
        'p95_ms': 300.0,
      },
      {
        't': 1761638400000,
        'runs': 12,
        'failures': 6,
        'uptime': 0.5,
        'p95_ms': 520.0,
      },
    ],
  },
  'failures': [
    {
      'timestamp': '2026-10-28T08:55:00.000000000Z',
      'location': 'local',
      'status_code': 503,
      'error_kind': 'status_code',
      'error': 'HTTP 503, expected 200',
      'duration_ms': 240.0,
    },
  ],
};

class ScriptedDetail extends SyntheticDetailController {
  ScriptedDetail() : super(_client, _checkId) {
    value = SyntheticResults.fromJson(_results());
    loaded = true;
  }
  @override
  Future<void> refresh() async {}
}

/// The synthetics half of the template catalog, as the server would filter it.
class ScriptedTemplates extends TemplatesController {
  ScriptedTemplates() : super(_client) {
    items = [
      AlertTemplate.fromJson({
        'id': 'synthetic_check_failing',
        'category': 'synthetics',
        'rule_type': 'metric_threshold',
        'severity': 'critical',
        'metric': 'synthetics.check.success',
        'name': {
          'en': 'Synthetic check failing',
          'tr': 'Sentetik kontrol başarısız',
        },
        'description': {'en': 'Below the threshold.', 'tr': 'Eşiğin altında.'},
        'params': [
          {
            'key': 'check_id',
            'kind': 'synthetic_check',
            'required': false,
            'default': '',
            'label': {'en': 'Check', 'tr': 'Kontrol'},
          },
          {
            'key': 'check_name',
            'kind': 'text',
            'required': false,
            'default': '',
            'label': {'en': 'Check name', 'tr': 'Kontrol adı'},
          },
          {
            'key': 'threshold',
            'kind': 'number',
            'unit': 'ratio',
            'required': true,
            'default': 1.0,
            'min': 0.01,
            'max': 1.0,
            'label': {
              'en': 'Successful runs below',
              'tr': 'Başarılı koşu oranı şunun altında',
            },
          },
        ],
      }),
    ];
    loaded = true;
  }
  @override
  Future<void> refresh() async {}
}

Sections sectionsFor() => Sections(
  client: _client,
  syntheticDetail: (_) => ScriptedDetail(),
  templatesFor: (_) => ScriptedTemplates(),
);

Future<Sections> pump(WidgetTester tester) async {
  final sections = sectionsFor();
  addTearDown(sections.dispose);
  await tester.pumpWidget(
    MaterialApp(
      localizationsDelegates: L.localizationsDelegates,
      supportedLocales: L.supportedLocales,
      locale: const Locale('tr'),
      home: SyntheticScreen(
        session: SessionController(store: MemoryTokenStore()),
        sections: sections,
        id: _checkId,
        name: 'Checkout health',
      ),
    ),
  );
  await tester.pumpAndSettle();
  return sections;
}

void main() {
  testWidgets('shows the period, every location and what failed', (
    tester,
  ) async {
    await pump(tester);

    // The three numbers the web leads with, in the units the web uses.
    expect(find.text('95.8%'), findsOneWidget);
    expect(find.text('288'), findsOneWidget);
    expect(find.text('420 ms'), findsOneWidget);
    // A 5 s interval reads as seconds, not as a fraction of a minute.
    expect(find.text('5 s'), findsOneWidget);
    // The location's own state, and the reason the last run gave.
    expect(find.byKey(const Key('synthetic-location-local')), findsOneWidget);
    expect(find.text('Çalışmıyor'), findsOneWidget);
    expect(find.text('HTTP 503, expected 200'), findsNWidgets(2));
    expect(find.byKey(const Key('synthetic-latency')), findsOneWidget);
  });

  testWidgets(
    'the alert shortcut opens the templates with the check filled in',
    (tester) async {
      await pump(tester);

      await tester.tap(find.byKey(const Key('synthetic-create-alert')));
      await tester.pumpAndSettle();

      // The synthetics templates, named by the check they are for.
      expect(find.text('Alarm kur'), findsOneWidget);
      expect(find.text('Checkout health'), findsOneWidget);
      expect(find.text('Sentetik kontrol başarısız'), findsOneWidget);
      // No category chips: this list was opened with its category chosen.
      expect(find.byKey(const Key('templates-category-all')), findsNothing);

      await tester.tap(
        find.byKey(const Key('template-synthetic_check_failing')),
      );
      await tester.pumpAndSettle();

      // The check is not asked for again -- only the threshold is left.
      expect(find.byType(TextField), findsOneWidget);
    },
  );
}
