// A host's other three screens, reached from the host screen: its charts, its
// whole inventory and its vulnerable packages.
//
// The web has these as tabs of one page. A phone's first screen for a host is
// already long, and each of these is its own round of requests, so here they
// wait to be asked for.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../infra_detail.dart';
import '../sections.dart';
import '../session.dart';
import 'container_screen.dart' show formatBytes;
import 'detail_scaffold.dart';
import 'infra_sort.dart';
import 'inventory_screen.dart' show inventoryCategoryLabel;
import 'list_paging.dart';
import 'list_scaffold.dart';
import 'monitor_screens.dart';
import 'oql_view.dart' show formatNumber;
import 'sparkline.dart';
import 'theme.dart';

class HostMetricsScreen extends StatefulWidget {
  const HostMetricsScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.hostId,
    required this.hostName,
  });

  final SessionController session;
  final Sections sections;
  final String hostId;
  final String hostName;

  @override
  State<HostMetricsScreen> createState() => _HostMetricsScreenState();
}

class _HostMetricsScreenState extends State<HostMetricsScreen> {
  late final _c = HostMetricsController(widget.sections.client, widget.hostId);

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => _c.refresh());
  }

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return ListenableBuilder(
      listenable: _c,
      builder: (context, _) => DetailScreen<List<HostChart>>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: l.hostTabMetrics,
        subtitle: widget.hostName,
        builder: (context, charts) => [
          for (final chart in charts) _ChartCard(chart: chart),
        ],
      ),
    );
  }
}

String hostChartTitle(L l, String id) => switch (id) {
  'cpu' => l.hostChartCpu,
  'load' => l.hostChartLoad,
  'memory' => l.hostChartMemory,
  'filesystem' => l.hostChartFilesystem,
  'disk' => l.hostChartDisk,
  'network' => l.hostChartNetwork,
  _ => id,
};

String formatChartValue(HostChartUnit unit, double? v) {
  if (v == null) return '–';
  return switch (unit) {
    HostChartUnit.percent => '${(v * 100).toStringAsFixed(1)}%',
    HostChartUnit.number => formatNumber(v),
    HostChartUnit.bytes => formatBytes(v),
    HostChartUnit.bytesPerSecond => '${formatBytes(v)}/s',
  };
}

/// One chart: its lines on one scale, and the latest of each under them.
///
/// Up to four lines drawn: a chart with twenty mountpoints is a smear on a
/// phone, but the numbers under it are all there, in the web's order.
class _ChartCard extends StatelessWidget {
  const _ChartCard({required this.chart});

  final HostChart chart;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final lines = chart.lines;
    final drawn = chart.id == 'cpu' || chart.id == 'memory'
        ? lines.take(1).toList()
        : lines.take(4).toList();
    final all = [for (final d in drawn) ...d.values];
    final palette = [
      colors.primary,
      colors.mutedForeground,
      colors.primary.withValues(alpha: 0.5),
      colors.mutedForeground.withValues(alpha: 0.5),
    ];

    return DetailSection(
      key: Key('host-chart-${chart.id}'),
      title: hostChartTitle(l, chart.id),
      children: [
        if (chart.error != null)
          Text(
            chart.error!,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.error,
            ),
          )
        else if (all.length < 2)
          Text(l.containerNoSeries, style: muted)
        else ...[
          SizedBox(
            height: 56,
            child: Stack(
              children: [
                for (final (i, d) in drawn.indexed)
                  Sparkline(
                    values: d.values,
                    color: palette[i % palette.length],
                    minimum: 0,
                    maximum: all.reduce((a, b) => a > b ? a : b),
                  ),
              ],
            ),
          ),
          const SizedBox(height: 8),
          for (final line in lines)
            KeyValue(
              name: line.label.isEmpty ? l.hostChartBusy : line.label,
              value: formatChartValue(chart.unit, line.latest),
            ),
        ],
      ],
    );
  }
}

class HostInventoryScreen extends StatefulWidget {
  const HostInventoryScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.hostId,
    required this.hostName,
  });

  final SessionController session;
  final Sections sections;
  final String hostId;
  final String hostName;

  @override
  State<HostInventoryScreen> createState() => _HostInventoryScreenState();
}

class _HostInventoryScreenState extends State<HostInventoryScreen> {
  late final _c = HostInventoryController(
    widget.sections.client,
    widget.hostId,
  );
  final _search = TextEditingController();

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => _c.refresh());
  }

  @override
  void dispose() {
    _c.dispose();
    _search.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return ListenableBuilder(
      listenable: _c,
      builder: (context, _) => DetailScreen<InventoryResponse>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: l.hostTabInventory,
        subtitle: widget.hostName,
        builder: (context, inv) => _body(context, inv),
      ),
    );
  }

  List<Widget> _body(BuildContext context, InventoryResponse inv) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    // Told apart as on the web: a host that never sent a whole snapshot, and
    // one whose snapshot is empty.
    if (inv.snapshotId.isEmpty) return [Text(l.hostInvNoSnapshot)];
    if (inv.items.isEmpty) return [Text(l.hostInvEmpty)];
    final filtered = _c.filtered;

    return [
      SingleChildScrollView(
        scrollDirection: Axis.horizontal,
        child: Row(
          children: [
            for (final (category, count, name) in [
              ('', inv.items.length, l.hostInvAll),
              for (final c in countByCategory(inv.items))
                (c.category, c.count, inventoryCategoryLabel(l, c.category)),
            ])
              Padding(
                padding: const EdgeInsets.only(right: 6),
                child: ChoiceChip(
                  key: Key('inv-category-$category'),
                  label: Text('$name $count'),
                  selected: _c.category == category,
                  onSelected: (_) => _c.filter(category: category),
                ),
              ),
          ],
        ),
      ),
      const SizedBox(height: 8),
      TextField(
        key: const Key('host-inventory-search'),
        controller: _search,
        onChanged: (v) => _c.filter(query: v),
        decoration: InputDecoration(
          hintText: l.hostInvSearch,
          prefixIcon: const Icon(Icons.search),
          border: const OutlineInputBorder(),
          isDense: true,
        ),
      ),
      const SizedBox(height: 6),
      Text(l.hostInvCount(filtered.length, inv.items.length), style: muted),
      const SizedBox(height: 8),
      if (filtered.isEmpty) Text(l.hostInvNoMatch),
      for (final item in filtered)
        Padding(
          key: Key('host-inv-${item.category}-${item.key}'),
          padding: const EdgeInsets.only(bottom: 10),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(inventoryKey(item), style: theme.textTheme.bodyMedium),
              Text(
                [
                  if (_c.category.isEmpty)
                    inventoryCategoryLabel(l, item.category),
                  if (summarizeInventory(item.data).isNotEmpty)
                    summarizeInventory(item.data),
                ].join(' · '),
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: muted,
              ),
            ],
          ),
        ),
    ];
  }
}

class HostVulnerabilitiesScreen extends StatefulWidget {
  const HostVulnerabilitiesScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.hostId,
    required this.hostName,
  });

  final SessionController session;
  final Sections sections;
  final String hostId;
  final String hostName;

  @override
  State<HostVulnerabilitiesScreen> createState() =>
      _HostVulnerabilitiesScreenState();
}

class _HostVulnerabilitiesScreenState extends State<HostVulnerabilitiesScreen> {
  late final _c = HostVulnerabilitiesController(
    widget.sections.client,
    widget.hostId,
  );

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => _c.refresh());
  }

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return Scaffold(
      appBar: AppBar(
        title: Text(l.hostTabVulns, overflow: TextOverflow.ellipsis),
      ),
      body: ListenableBuilder(
        listenable: _c,
        builder: (context, _) => ListScreen<VulnFinding>(
          controller: _c,
          baseUrl: widget.session.baseUrl ?? '',
          emptyTitle: _c.notOffered
              ? l.serverNotOffered
              : '${l.vulnHostEmpty}\n${l.vulnHostEmptyHint}',
          search: Align(
            alignment: Alignment.centerLeft,
            child: ListSortBar(
              controller: _c,
              columns: hostFindingSorts,
              label: (s) => infraSortLabel(l, s),
            ),
          ),
          footer: LoadMoreTile(controller: _c),
          itemBuilder: (context, i) => FindingCard(
            finding: _c.items[i],
            showHost: false,
            onOpen: () => Navigator.of(context).push(
              MaterialPageRoute<void>(
                builder: (_) => VulnerabilityScreen(
                  session: widget.session,
                  sections: widget.sections,
                  id: _c.items[i].vulnId,
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }
}
