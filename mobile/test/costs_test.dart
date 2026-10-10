// The costs screen: the decomposition, the trend and the services.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/l10n/app_localizations.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/detail.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';
import 'package:openlog_mobile/src/ui/costs_screen.dart';

final _client = OpenlogClient(baseUrl: 'http://127.0.0.1:1');

const _summary = CostSummary(
  currency: 'USD',
  total: 412.5,
  services: 210.0,
  unallocated: 64.0,
  unattributed: 38.5,
  idle: 100.0,
  idleShare: 0.242,
  perHour: 1.72,
  hosts: 12,
  pricedHosts: 11,
  unpricedHosts: 1,
  hostHours: 240,
);

const _pricing = CostPricing(
  version: 3,
  updated: '2026-09-17',
  currency: 'USD',
  note: 'indirimler, vergiler, depolama ve çıkış trafiği dahil değil',
  estimated: true,
);

class Costs extends CostsController {
  Costs() : super(_client) {
    value = CostHostPage(
      hosts: [
        CostHost(
          hostId: 'h1',
          hostName: 'web-1',
          provider: 'aws',
          instanceType: 'm5.large',
          region: 'eu-central-1',
          zone: 'eu-central-1a',
          lifecycle: 'on-demand',
          vcpus: 2,
          memoryBytes: 8589934592,
          hours: 24,
          price: const CostPrice(
            usdPerHour: 0.096,
            source: CostSource.table,
            regionMultiplier: 1,
          ),
          total: 2.3,
          services: 1.2,
          unallocated: 0.4,
          unattributed: 0.1,
          idle: 0.6,
          usedShare: 0.74,
          idleShare: 0.26,
          oversubscribed: false,
          priced: true,
        ),
      ],
      total: 12,
      summary: _summary,
      pricing: _pricing,
    );
    services = const [
      CostService(
        serviceName: 'checkout',
        serviceNamespace: '',
        environment: 'prod',
        total: 128.4,
        hosts: ['h1', 'h2'],
        containers: 6,
      ),
      CostService(
        serviceName: 'search',
        serviceNamespace: '',
        environment: 'prod',
        total: 51.2,
        hosts: ['h3'],
        containers: 2,
      ),
    ];
    trend = CostTrend(
      step: '1h',
      points: [
        for (var i = 0; i < 24; i++)
          CostTrendPoint(
            t: 1760000000000 + i * 3600000,
            total: 14 + (i % 7) * 1.5,
            idle: 3 + (i % 5) * 0.4,
          ),
      ],
      pricing: _pricing,
      from: 1760000000000,
      to: 1760086400000,
    );
    loaded = true;
  }

  @override
  Future<void> refresh() async {}
}

void main() {
  testWidgets('the bill is decomposed, with the trend and the services', (
    tester,
  ) async {
    final costs = Costs();
    addTearDown(costs.dispose);
    await tester.pumpWidget(
      MaterialApp(
        localizationsDelegates: L.localizationsDelegates,
        supportedLocales: L.supportedLocales,
        locale: const Locale('tr'),
        home: Scaffold(
          body: CostsBody(
            session: SessionController(store: MemoryTokenStore()),
            costs: costs,
            active: true,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    // The four buckets add up to the total, so the bar is a
    // decomposition: four pieces, not an illustration.
    final bar = find.byKey(const Key('cost-buckets'));
    expect(bar, findsOneWidget);
    expect(
      find.descendant(of: bar, matching: find.byType(ColoredBox)),
      findsNWidgets(4),
    );
    // A run rate of 1.72 an hour must not round to 2.
    expect(find.text('1.72'), findsOneWidget);
    // One list, one kind of number: the legend drops the cents because
    // its biggest row is in the hundreds.
    expect(find.text('Servisler · 210'), findsOneWidget);
    expect(find.text('Boşta · 100'), findsOneWidget);

    await tester.drag(find.text('1.72'), const Offset(0, -500));
    await tester.pumpAndSettle();
    // Both lines of the trend are drawn against one scale.
    expect(find.byKey(const Key('cost-trend-total')), findsOneWidget);
    expect(find.byKey(const Key('cost-trend-idle')), findsOneWidget);
    expect(find.text('checkout'), findsOneWidget);
  });
}
