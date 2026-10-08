// Service health: the list an alert sends you to.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../services.dart';
import '../session.dart';
import 'list_scaffold.dart';

class ServicesBody extends StatefulWidget {
  const ServicesBody({
    super.key,
    required this.session,
    required this.services,
  });

  final SessionController session;
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
        search: SearchField(
          fieldKey: const Key('services-search'),
          controller: _search,
          hint: l.servicesSearch,
          onSubmitted: (value) {
            c.query = value;
            c.refresh();
          },
        ),
        emptyTitle: l.servicesEmpty,
        itemBuilder: (context, i) => _ServiceCard(service: c.items[i]),
      ),
    );
  }
}

class _ServiceCard extends StatelessWidget {
  const _ServiceCard({required this.service});

  final ApmService service;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final text = Theme.of(context).textTheme;
    final scheme = Theme.of(context).colorScheme;
    final errorPercent = service.errorRate * 100;

    return Card(
      key: Key('service-${service.serviceName}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
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
                  label: l.svcThroughput,
                  value: service.throughput.toStringAsFixed(0),
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
              ],
            ),
          ],
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
