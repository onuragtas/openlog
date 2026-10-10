// What calls this service, and what it calls.
//
// The web draws the service map as a graph. A phone draws the same edges as
// two lists -- callers above, dependencies below -- because a node-link
// picture of forty services on a 390-point screen is a picture of nothing.
// Every number the map's edges carry is here; only the drawing is different.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../services.dart';
import '../session.dart';
import 'failure_text.dart';
import 'service_screen.dart';
import 'severity.dart';

class ServiceMapTab extends StatefulWidget {
  const ServiceMapTab({
    super.key,
    required this.session,
    required this.sections,
    required this.controller,
  });

  final SessionController session;
  final Sections sections;
  final ServiceMapController controller;

  @override
  State<ServiceMapTab> createState() => _ServiceMapTabState();
}

class _ServiceMapTabState extends State<ServiceMapTab> {
  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        if (c.loadingFirst) {
          return const Center(child: CircularProgressIndicator());
        }
        final incoming = c.incoming;
        final outgoing = c.outgoing;
        return RefreshIndicator(
          onRefresh: c.refresh,
          child: ListView(
            padding: const EdgeInsets.fromLTRB(16, 12, 16, 28),
            children: [
              FailureBanner(
                failure: c.failure,
                baseUrl: widget.session.baseUrl ?? '',
              ),
              if (c.pathTransaction.isNotEmpty)
                Card(
                  key: const Key('map-path'),
                  margin: const EdgeInsets.only(bottom: 10),
                  child: Padding(
                    padding: const EdgeInsets.fromLTRB(12, 10, 4, 10),
                    child: Row(
                      children: [
                        Expanded(
                          child: Text(
                            l.mapPathOf(c.pathTransaction, c.pathTraces),
                            style: theme.textTheme.bodySmall,
                          ),
                        ),
                        IconButton(
                          key: const Key('map-path-clear'),
                          tooltip: l.mapPathClear,
                          onPressed: c.clearPath,
                          icon: const Icon(Icons.close, size: 18),
                        ),
                      ],
                    ),
                  ),
                ),
              if (incoming.isEmpty && outgoing.isEmpty)
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 36),
                  child: Center(
                    child: Text(
                      l.mapEmpty,
                      key: const Key('map-empty'),
                      style: theme.textTheme.bodyMedium?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                  ),
                ),
              if (incoming.isNotEmpty) ...[
                Text(l.mapIncoming, style: theme.textTheme.titleSmall),
                Text(
                  l.mapIncomingHint,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
                for (final e in incoming)
                  _EdgeCard(
                    edge: e,
                    other: c.nodes[e.source],
                    onPath: c.pathEdges.contains(e.id),
                    dimmed:
                        c.pathTransaction.isNotEmpty &&
                        !c.pathEdges.contains(e.id),
                    onOpen: _opener(c.nodes[e.source]),
                  ),
                const SizedBox(height: 14),
              ],
              if (outgoing.isNotEmpty) ...[
                Text(l.mapOutgoing, style: theme.textTheme.titleSmall),
                Text(
                  l.mapOutgoingHint,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
                for (final e in outgoing)
                  _EdgeCard(
                    edge: e,
                    other: c.nodes[e.target],
                    onPath: c.pathEdges.contains(e.id),
                    dimmed:
                        c.pathTransaction.isNotEmpty &&
                        !c.pathEdges.contains(e.id),
                    onOpen: _opener(c.nodes[e.target]),
                  ),
              ],
            ],
          ),
        );
      },
    );
  }

  /// Only another service can be opened: a database or an external host is
  /// an edge of the map, not a page of this console.
  VoidCallback? _opener(ApmMapNode? node) {
    if (node == null || node.type != ApmMapNodeType.service) return null;
    return () => Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => ServiceScreen(
          session: widget.session,
          sections: widget.sections,
          serviceName: node.name,
        ),
      ),
    );
  }
}

class _EdgeCard extends StatelessWidget {
  const _EdgeCard({
    required this.edge,
    required this.other,
    required this.onPath,
    required this.dimmed,
    required this.onOpen,
  });

  final ApmMapEdge edge;
  final ApmMapNode? other;

  /// On the path of the transaction somebody asked about.
  final bool onPath;

  /// A transaction was asked about and this edge is not on its path.
  final bool dimmed;
  final VoidCallback? onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final kind = switch (other?.type) {
      ApmMapNodeType.db => l.mapKindDb,
      ApmMapNodeType.external => l.mapKindExternal,
      ApmMapNodeType.messaging => l.mapKindMessaging,
      _ => '',
    };

    return Opacity(
      // Dimmed rather than hidden: an edge the transaction does not use is
      // still a dependency of the service, and hiding it would make the
      // list mean something different depending on a filter.
      opacity: dimmed ? 0.45 : 1,
      child: Card(
        key: Key('edge-${edge.id}'),
        margin: const EdgeInsets.symmetric(vertical: 4),
        child: InkWell(
          onTap: onOpen,
          borderRadius: BorderRadius.circular(12),
          child: Padding(
            padding: const EdgeInsets.all(12),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    Expanded(
                      child: Text(
                        other?.name ?? edge.target,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: theme.textTheme.titleSmall,
                      ),
                    ),
                    if (onPath) ...[
                      const SizedBox(width: 6),
                      Tag(label: l.mapOnPath, level: SeverityLevel.info),
                    ],
                    if (kind.isNotEmpty) ...[
                      const SizedBox(width: 6),
                      Text(kind, style: muted),
                    ],
                  ],
                ),
                const SizedBox(height: 4),
                Wrap(
                  spacing: 10,
                  runSpacing: 2,
                  children: [
                    Text(l.mapCalls(edge.calls.round()), style: muted),
                    Text(
                      l.mapErrorRate(_percent(edge.errorRate)),
                      style: edge.errorRate > 0
                          ? theme.textTheme.bodySmall?.copyWith(
                              color: severityTextColor(
                                context,
                                SeverityLevel.critical,
                              ),
                            )
                          : muted,
                    ),
                    if (edge.p95Ms != null)
                      Text(l.mapP95(_ms(edge.p95Ms!)), style: muted),
                    if (edge.avgMs != null)
                      Text(l.mapAvg(_ms(edge.avgMs!)), style: muted),
                  ],
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

String _percent(double ratio) {
  final v = ratio * 100;
  if (v == 0) return '0';
  return v >= 10 ? v.toStringAsFixed(0) : v.toStringAsFixed(1);
}

/// Milliseconds with at most one decimal, and none when it would be a zero:
/// "88.0 ms" claims a precision the number does not have.
String _ms(double v) {
  if (v >= 100 || v == v.roundToDouble()) return v.toStringAsFixed(0);
  return v.toStringAsFixed(1);
}
