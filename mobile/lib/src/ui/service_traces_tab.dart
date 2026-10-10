// The entry spans of one service: the web's traces tab, with its filters.
//
// The filters are folded away behind a summary line, because on a phone six
// fields above a list would leave no list. They open where they were last
// left: somebody who filtered by transaction is about to filter again.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../services.dart';
import '../session.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'severity.dart';
import 'theme.dart';
import 'trace_screen.dart';

class ServiceTracesTab extends StatefulWidget {
  const ServiceTracesTab({
    super.key,
    required this.session,
    required this.sections,
    required this.controller,
    this.onShowPath,
  });

  final SessionController session;
  final Sections sections;
  final ServiceTracesController controller;

  /// Asks the dependencies tab which of the map a transaction goes through.
  final void Function(String transaction)? onShowPath;

  @override
  State<ServiceTracesTab> createState() => _ServiceTracesTabState();
}

class _ServiceTracesTabState extends State<ServiceTracesTab> {
  final _transaction = TextEditingController();
  final _min = TextEditingController();
  final _max = TextEditingController();
  final _attributes = TextEditingController();

  @override
  void dispose() {
    _transaction.dispose();
    _min.dispose();
    _max.dispose();
    _attributes.dispose();
    super.dispose();
  }

  void _apply() {
    final c = widget.controller;
    c.transaction = _transaction.text.trim();
    c.minDurationMs = _min.text.trim();
    c.maxDurationMs = _max.text.trim();
    c.attributes = parseAttributeFilter(_attributes.text);
    c.refresh();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => Column(
        children: [
          ExpansionTile(
            key: const Key('service-traces-filters'),
            title: Text(l.tracesFilters, style: theme.textTheme.bodyMedium),
            childrenPadding: const EdgeInsets.fromLTRB(16, 0, 16, 12),
            children: [
              TextField(
                key: const Key('service-traces-transaction'),
                controller: _transaction,
                decoration: InputDecoration(
                  labelText: l.tracesTransaction,
                  hintText: 'GET /orders/{id}',
                  border: const OutlineInputBorder(),
                  isDense: true,
                ),
              ),
              const SizedBox(height: 10),
              Row(
                children: [
                  Expanded(
                    child: TextField(
                      key: const Key('service-traces-min'),
                      controller: _min,
                      keyboardType: const TextInputType.numberWithOptions(
                        decimal: true,
                      ),
                      decoration: InputDecoration(
                        labelText: l.tracesMinMs,
                        border: const OutlineInputBorder(),
                        isDense: true,
                      ),
                    ),
                  ),
                  const SizedBox(width: 10),
                  Expanded(
                    child: TextField(
                      key: const Key('service-traces-max'),
                      controller: _max,
                      keyboardType: const TextInputType.numberWithOptions(
                        decimal: true,
                      ),
                      decoration: InputDecoration(
                        labelText: l.tracesMaxMs,
                        border: const OutlineInputBorder(),
                        isDense: true,
                      ),
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 10),
              TextField(
                key: const Key('service-traces-attributes'),
                controller: _attributes,
                decoration: InputDecoration(
                  labelText: l.tracesAttributes,
                  hintText: 'http.method=POST',
                  helperText: l.tracesAttributesHint,
                  helperMaxLines: 2,
                  border: const OutlineInputBorder(),
                  isDense: true,
                ),
              ),
              const SizedBox(height: 6),
              Row(
                children: [
                  Expanded(
                    child: SwitchListTile(
                      key: const Key('service-traces-errors'),
                      contentPadding: EdgeInsets.zero,
                      dense: true,
                      title: Text(
                        l.tracesErrorsOnly,
                        style: theme.textTheme.bodySmall,
                      ),
                      value: c.errorsOnly,
                      onChanged: (v) {
                        setState(() => c.errorsOnly = v);
                        c.refresh();
                      },
                    ),
                  ),
                ],
              ),
              SegmentedButton<String>(
                key: const Key('service-traces-sort'),
                showSelectedIcon: false,
                segments: [
                  ButtonSegment(
                    value: 'timestamp',
                    label: Text(l.tracesNewest),
                  ),
                  ButtonSegment(
                    value: 'duration',
                    label: Text(l.tracesSlowest),
                  ),
                ],
                selected: {c.sort},
                onSelectionChanged: (s) {
                  setState(() => c.sort = s.first);
                  c.refresh();
                },
              ),
              const SizedBox(height: 10),
              Align(
                alignment: Alignment.centerRight,
                child: FilledButton(
                  key: const Key('service-traces-apply'),
                  onPressed: _apply,
                  child: Text(l.tracesSearch),
                ),
              ),
            ],
          ),
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 16),
            child: FailureBanner(
              failure: c.failure,
              baseUrl: widget.session.baseUrl ?? '',
            ),
          ),
          Expanded(
            child: c.loadingFirst
                ? const Center(child: CircularProgressIndicator())
                : c.items.isEmpty
                ? Center(
                    child: Text(
                      l.tracesEmpty,
                      key: const Key('service-traces-empty'),
                      style: theme.textTheme.bodyMedium?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                  )
                : RefreshIndicator(
                    onRefresh: c.refresh,
                    child: ListView.builder(
                      padding: const EdgeInsets.fromLTRB(12, 4, 12, 24),
                      itemCount: c.items.length,
                      itemBuilder: (context, i) => _ApmTraceCard(
                        trace: c.items[i],
                        onShowPath: widget.onShowPath == null
                            ? null
                            : () => widget.onShowPath!(
                                c.items[i].transactionName,
                              ),
                        onOpen: () => Navigator.of(context).push(
                          MaterialPageRoute<void>(
                            builder: (_) => TraceScreen(
                              session: widget.session,
                              sections: widget.sections,
                              traceId: c.items[i].traceId,
                            ),
                          ),
                        ),
                      ),
                    ),
                  ),
          ),
        ],
      ),
    );
  }
}

class _ApmTraceCard extends StatelessWidget {
  const _ApmTraceCard({
    required this.trace,
    required this.onOpen,
    this.onShowPath,
  });

  final ApmTraceResult trace;
  final VoidCallback onOpen;
  final VoidCallback? onShowPath;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return Card(
      key: Key('apm-trace-${trace.traceId}'),
      margin: const EdgeInsets.symmetric(vertical: 5),
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
                    child: Text(
                      trace.transactionName,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.titleSmall?.copyWith(
                        color: trace.isError
                            ? severityTextColor(context, SeverityLevel.critical)
                            : null,
                      ),
                    ),
                  ),
                  const SizedBox(width: 8),
                  Text(
                    '${trace.durationMs.toStringAsFixed(trace.durationMs >= 100 ? 0 : 1)} ms',
                    style: theme.textTheme.bodyMedium,
                  ),
                  if (onShowPath != null)
                    IconButton(
                      key: Key('trace-path-${trace.traceId}'),
                      tooltip: l.mapShowPath,
                      visualDensity: VisualDensity.compact,
                      onPressed: onShowPath,
                      icon: const Icon(Icons.account_tree_outlined, size: 18),
                    ),
                ],
              ),
              const SizedBox(height: 6),
              Wrap(
                spacing: 8,
                runSpacing: 4,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  Text(relativeTimeOf(l, trace.timestamp), style: muted),
                  if (trace.transactionType.isNotEmpty)
                    Text(trace.transactionType, style: muted),
                  if (trace.httpStatusCode > 0)
                    Text(
                      '${trace.httpStatusCode}',
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: trace.httpStatusCode >= 500
                            ? severityTextColor(context, SeverityLevel.critical)
                            : theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                  if (trace.isError)
                    Tag(label: l.tracesError, level: SeverityLevel.critical),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }
}
