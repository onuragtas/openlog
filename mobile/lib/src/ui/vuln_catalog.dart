// What the advisory catalog holds, under the vulnerability list.
//
// An empty list of findings has three possible meanings and only one of
// them is good news: nothing is vulnerable, the feed never synced so
// nothing was compared, or this installation keeps no catalog at all. The
// list cannot tell them apart; this line can.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../sections.dart';
import 'list_scaffold.dart' show relativeTimeOf;
import 'oql_view.dart' show formatNumber;
import 'severity.dart';

class VulnCatalogNote extends StatelessWidget {
  const VulnCatalogNote({super.key, required this.controller});

  final VulnerabilitiesController controller;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final c = controller;

    if (c.catalogUnavailable) {
      return Padding(
        padding: const EdgeInsets.only(bottom: 10),
        child: Text(l.vulnCatalogUnavailable, style: muted),
      );
    }
    final status = c.catalog;
    if (status == null) return const SizedBox.shrink();

    final synced = status.lastSyncedAt;
    final errors = [
      for (final s in status.sources)
        if (s.error.isNotEmpty) s,
    ];

    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (synced == null)
            // The loud one: a list that was never compared against anything
            // reads as a clean installation, which is the opposite.
            Text(
              l.vulnCatalogNever,
              style: theme.textTheme.bodySmall?.copyWith(
                color: severityTextColor(context, SeverityLevel.warning),
              ),
            )
          else
            Text(
              l.vulnCatalogSynced(
                formatNumber(status.vulnerabilities.toDouble()),
                status.ecosystems,
                relativeTimeOf(l, synced),
              ),
              style: muted,
            ),
          // A feed that failed is why a number is older than it looks.
          for (final s in errors)
            Padding(
              padding: const EdgeInsets.only(top: 4),
              child: Text(
                l.vulnCatalogSourceError(s.source, s.error),
                style: theme.textTheme.bodySmall?.copyWith(
                  color: severityTextColor(context, SeverityLevel.critical),
                ),
              ),
            ),
        ],
      ),
    );
  }
}
