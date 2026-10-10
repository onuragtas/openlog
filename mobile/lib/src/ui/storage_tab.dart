// How full the ClickHouse disks are.
//
// The web's Depolama tab: one row per disk of each replica, the levels in
// force, and when the measurement was taken. Read-only here as there --
// changing the levels is the operator's, server-side.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../session.dart';
import '../usage.dart';
import 'failure_text.dart';
import 'severity.dart';

class StorageTab extends StatelessWidget {
  const StorageTab({
    super.key,
    required this.session,
    required this.controller,
  });

  final SessionController session;
  final StorageController controller;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        if (c.loading) {
          return const Center(child: CircularProgressIndicator());
        }
        final space = c.space;
        return RefreshIndicator(
          onRefresh: c.load,
          child: ListView(
            padding: const EdgeInsets.all(16),
            children: [
              FailureBanner(failure: c.failure, baseUrl: session.baseUrl ?? ''),
              if (space != null) ...[
                if (space.checkedAt.isEmpty)
                  Text(
                    // Nothing has been measured yet, which is a different
                    // thing from disks that are empty.
                    l.storageNotMeasured,
                    key: const Key('storage-not-measured'),
                    style: theme.textTheme.bodyMedium?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  )
                else ...[
                  Text(
                    l.storageLevels(
                      space.effective.warnPercent,
                      space.effective.highPercent,
                    ),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                  const SizedBox(height: 10),
                  for (final d in space.disks) _Disk(disk: d, space: space),
                ],
              ],
            ],
          ),
        );
      },
    );
  }
}

class _Disk extends StatelessWidget {
  const _Disk({required this.disk, required this.space});

  final DiskStatus disk;
  final DiskSpace space;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    // The level the server reported it at, not a threshold this app
    // re-derives: the two could disagree after a configuration change.
    final level = disk.broken
        ? SeverityLevel.critical
        : disk.level >= 2
        ? SeverityLevel.critical
        : disk.level == 1
        ? SeverityLevel.warning
        : SeverityLevel.good;

    return Card(
      key: Key('disk-${disk.host}-${disk.disk}'),
      margin: const EdgeInsets.symmetric(vertical: 4),
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(
                    '${disk.host} · ${disk.disk}',
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.bodyMedium,
                  ),
                ),
                Text(
                  '${disk.usedPercent.round()}%',
                  style: theme.textTheme.bodyMedium?.copyWith(
                    color: severityTextColor(context, level),
                  ),
                ),
              ],
            ),
            const SizedBox(height: 4),
            ClipRRect(
              borderRadius: BorderRadius.circular(4),
              child: LinearProgressIndicator(
                value: (disk.usedPercent / 100).clamp(0, 1),
                minHeight: 6,
                backgroundColor: theme.colorScheme.surfaceContainerHighest,
                color: severityTextColor(context, level),
              ),
            ),
            const SizedBox(height: 4),
            Text(
              disk.broken
                  ? l.storageBroken
                  : l.storageFree(
                      formatBytes(disk.freeBytes),
                      formatBytes(disk.totalBytes),
                    ),
              style: theme.textTheme.bodySmall?.copyWith(
                color: disk.broken
                    ? severityTextColor(context, SeverityLevel.critical)
                    : theme.colorScheme.onSurfaceVariant,
              ),
            ),
          ],
        ),
      ),
    );
  }
}
