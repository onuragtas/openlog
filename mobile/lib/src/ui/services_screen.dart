// Service health: the list an alert sends you to.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../apm_format.dart';
import '../sections.dart';
import '../services.dart';
import '../session.dart';
import 'agents_screen.dart';
import 'errors_screen.dart';
import 'list_paging.dart';
import 'list_scaffold.dart';
import 'service_screen.dart';
import 'theme.dart';

class ServicesBody extends StatefulWidget {
  const ServicesBody({
    super.key,
    required this.session,
    required this.sections,
    required this.services,
  });

  final SessionController session;

  /// Needed to open a service: the detail screen makes its own controller.
  final Sections sections;
  final ServicesController services;

  @override
  State<ServicesBody> createState() => _ServicesBodyState();
}

class _ServicesBodyState extends State<ServicesBody> {
  final _search = TextEditingController();

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback(
      (_) => widget.services.refresh(),
    );
  }

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.services;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => ListScreen(
        controller: c,
        baseUrl: widget.session.baseUrl ?? '',
        search: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            SearchField(
              fieldKey: const Key('services-search'),
              controller: _search,
              hint: l.servicesSearch,
              onSubmitted: (value) {
                c.query = value;
                c.refresh();
              },
            ),
            ListSortBar(
              controller: c,
              columns: serviceSorts,
              label: (s) => switch (s) {
                'environment' => l.apmSortEnvironment,
                'throughput' => l.apmSortThroughput,
                'error_rate' => l.apmSortErrorRate,
                'p95' => 'p95',
                'apdex' => l.svcApdex,
                'last_seen' => l.apmSortLastSeen,
                _ => l.apmSortService,
              },
            ),
          ],
        ),
        header: Wrap(
          spacing: 4,
          children: [
            TextButton.icon(
              key: const Key('services-errors'),
              onPressed: () => Navigator.of(context).push(
                MaterialPageRoute<void>(
                  builder: (_) => ErrorsScreen(
                    session: widget.session,
                    sections: widget.sections,
                  ),
                ),
              ),
              icon: const Icon(Icons.bug_report_outlined),
              label: Text(l.navErrors),
            ),
            TextButton.icon(
              key: const Key('services-agents'),
              onPressed: () => Navigator.of(context).push(
                MaterialPageRoute<void>(
                  builder: (_) => AgentsScreen(
                    session: widget.session,
                    agents: widget.sections.agents,
                  ),
                ),
              ),
              icon: const Icon(Icons.extension_outlined),
              label: Text(l.servicesAgents),
            ),
          ],
        ),
        emptyTitle: l.servicesEmpty,
        footer: LoadMoreTile(controller: c),
        itemBuilder: (context, i) => _ServiceCard(
          service: c.items[i],
          onOpen: () => Navigator.of(context).push(
            MaterialPageRoute<void>(
              builder: (_) => ServiceScreen(
                session: widget.session,
                sections: widget.sections,
                serviceName: c.items[i].serviceName,
              ),
            ),
          ),
        ),
      ),
    );
  }
}

class _ServiceCard extends StatelessWidget {
  const _ServiceCard({required this.service, required this.onOpen});

  final ApmService service;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final text = Theme.of(context).textTheme;
    final scheme = Theme.of(context).colorScheme;
    final errorPercent = service.errorRate * 100;

    return Card(
      key: Key('service-${service.serviceName}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: InkWell(
        onTap: onOpen,
        borderRadius: BorderRadius.circular(Radii.lg),
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Expanded(
                    child: Text(service.serviceName, style: text.titleMedium),
                  ),
                  if (service.environment.isNotEmpty)
                    Text(
                      service.environment,
                      style: text.bodySmall?.copyWith(
                        color: scheme.onSurfaceVariant,
                      ),
                    ),
                ],
              ),
              const SizedBox(height: 10),
              Wrap(
                spacing: 18,
                runSpacing: 6,
                children: [
                  _Stat(
                    label: l.apmSortThroughput,
                    value: formatApmRpm(service.throughput),
                  ),
                  _Stat(
                    label: l.svcErrorRate,
                    value:
                        '${errorPercent.toStringAsFixed(errorPercent >= 10 ? 0 : 1)}%',
                    // Red only when there are errors: colouring every row red
                    // because the column is called "errors" makes the colour
                    // mean nothing on the row that actually has them.
                    emphasis: service.errorRate > 0 ? scheme.error : null,
                  ),
                  _Stat(label: l.svcP95, value: _ms(l, service.p95Ms)),
                  _Stat(
                    label: l.svcApdex,
                    value: service.apdex?.toStringAsFixed(2) ?? l.svcNoData,
                  ),
                  _Stat(
                    label: l.apmSortLastSeen,
                    value: relativeTimeOf(l, service.lastSeen),
                  ),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }

  String _ms(L l, double? value) =>
      value == null ? l.svcNoData : '${value.toStringAsFixed(0)} ms';
}

class _Stat extends StatelessWidget {
  const _Stat({required this.label, required this.value, this.emphasis});

  final String label;
  final String value;
  final Color? emphasis;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final scheme = Theme.of(context).colorScheme;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      mainAxisSize: MainAxisSize.min,
      children: [
        Text(
          label,
          style: text.bodySmall?.copyWith(color: scheme.onSurfaceVariant),
        ),
        Text(
          value,
          style: text.titleSmall?.copyWith(
            fontWeight: FontWeight.w600,
            color: emphasis,
          ),
        ),
      ],
    );
  }
}
