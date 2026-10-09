// Integrations, as the agents report them.
//
// The same inventory search the web's page is built on: the discovered
// service's body says whether the integration is collecting, so this screen
// reads a status rather than deriving one -- and cannot disagree with the web.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../discovery.dart';
import '../sections.dart';
import '../session.dart';
import 'sections_screen.dart';
import 'severity.dart';

class IntegrationsBody extends StatelessWidget {
  const IntegrationsBody({
    super.key,
    required this.session,
    required this.sections,
    required this.active,
  });

  final SessionController session;
  final Sections sections;
  final bool active;

  @override
  Widget build(BuildContext context) {
    final c = sections.integrations;

    return SectionBody<IntegrationInstance>(
      session: session,
      controller: c,
      searchKey: 'integrations-search',
      searchHint: (l) => l.integrationsSearch,
      active: active,
      emptyTitle: (l) => l.integrationsEmpty,
      header: ListenableBuilder(
        listenable: c,
        builder: (context, _) {
          if (c.items.isEmpty) return const SizedBox.shrink();
          final l = L.of(context);
          final n = c.counts;
          final theme = Theme.of(context);
          return Padding(
            padding: const EdgeInsets.only(bottom: 8, left: 4),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  l.integCounts(n.enabled, n.needsConfiguration, n.error),
                  style: theme.textTheme.bodySmall,
                ),
                Text(
                  l.integConfigureOnWeb,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              ],
            ),
          );
        },
      ),
      card: (context, i) => _InstanceCard(instance: i),
    );
  }
}

class _InstanceCard extends StatelessWidget {
  const _InstanceCard({required this.instance});

  final IntegrationInstance instance;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final integ = instance.service.integration;
    final (label, level) = switch (instance.status) {
      DiscoveredServiceIntegrationStatus.enabled => (
        l.integEnabled,
        SeverityLevel.good,
      ),
      DiscoveredServiceIntegrationStatus.needsConfiguration => (
        l.integNeedsConfig,
        SeverityLevel.warning,
      ),
      DiscoveredServiceIntegrationStatus.error => (
        l.integError,
        SeverityLevel.critical,
      ),
      _ => (l.integNotAvailable, SeverityLevel.unknown),
    };

    return Card(
      key: Key('integration-${instance.hostId}-${instance.key}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(
                    instance.name,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.titleSmall,
                  ),
                ),
                const SizedBox(width: 8),
                Tag(label: label, level: level),
              ],
            ),
            const SizedBox(height: 4),
            Text(instance.hostName, style: muted),
            // The invoked path when it differs from the matched executable:
            // redis-server and redis-check-rdb are the same binary, and the
            // key alone would name the wrong one.
            Text(
              instance.service.displayInstance ??
                  instance.service.instance ??
                  instance.key,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: muted,
            ),
            if (instance.service.version != null)
              Text(instance.service.version!, style: muted),
            if (integ?.endpoint != null && integ!.endpoint!.isNotEmpty)
              Text(integ.endpoint!, style: muted),
            // The error is set on a partial collection too, so it is worth
            // showing even next to "collecting".
            if (integ?.error != null && integ!.error!.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(top: 4),
                child: Text(
                  integ.error!,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.error,
                  ),
                ),
              ),
          ],
        ),
      ),
    );
  }
}
