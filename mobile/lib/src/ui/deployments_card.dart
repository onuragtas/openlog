// When each version of a service first appeared, and what one of them did.
//
// The web puts this on the service's overview as a table with a comparison
// panel. Here it is a list of rows, and tapping one opens the same
// comparison underneath it: RED and Apdex before against after, plus the
// error groups first seen since.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../services.dart';
import '../session.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'severity.dart';

class DeploymentsCard extends StatelessWidget {
  const DeploymentsCard({
    super.key,
    required this.session,
    required this.controller,
  });

  final SessionController session;
  final ServiceDeploymentsController controller;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return ListenableBuilder(
      listenable: controller,
      builder: (context, _) => Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const SizedBox(height: 18),
          Text(l.deploymentsTitle, style: theme.textTheme.titleSmall),
          FailureBanner(
            failure: controller.failure,
            baseUrl: session.baseUrl ?? '',
          ),
          if (controller.loadingFirst)
            const Padding(
              padding: EdgeInsets.symmetric(vertical: 16),
              child: Center(child: CircularProgressIndicator()),
            )
          else if (controller.items.isEmpty)
            Padding(
              padding: const EdgeInsets.symmetric(vertical: 10),
              child: Text(
                l.deploymentsEmpty,
                key: const Key('deployments-empty'),
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            )
          else
            for (final d in controller.items)
              _DeploymentRow(
                deployment: d,
                open: controller.comparing == d.t,
                busy: controller.comparingBusy && controller.comparing == d.t,
                compare: controller.comparing == d.t
                    ? controller.compare
                    : null,
                onTap: () => controller.toggleCompare(d.t),
              ),
        ],
      ),
    );
  }
}

class _DeploymentRow extends StatelessWidget {
  const _DeploymentRow({
    required this.deployment,
    required this.open,
    required this.busy,
    required this.compare,
    required this.onTap,
  });

  final ApmDeployment deployment;
  final bool open;
  final bool busy;
  final ApmDeploymentCompare? compare;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return Card(
      key: Key('deployment-${deployment.t}'),
      margin: const EdgeInsets.symmetric(vertical: 4),
      child: InkWell(
        onTap: onTap,
        borderRadius: BorderRadius.circular(12),
        child: Padding(
          padding: const EdgeInsets.all(12),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  // The change, not just the version: somebody is looking
                  // for "what replaced what", and the first version ever has
                  // nothing to point from. An icon rather than an arrow
                  // character, as the web uses -- a glyph the font does not
                  // have draws as a box.
                  if (!deployment.initial) ...[
                    Flexible(
                      child: Text(
                        _version(deployment.previousVersion),
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: theme.textTheme.bodyMedium?.copyWith(
                          color: theme.colorScheme.onSurfaceVariant,
                        ),
                      ),
                    ),
                    const _Arrow(),
                  ],
                  Flexible(
                    child: Text(
                      _version(deployment.version),
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.titleSmall,
                    ),
                  ),
                  const Spacer(),
                  if (deployment.rollback) ...[
                    const SizedBox(width: 6),
                    Tag(
                      label: l.deploymentsRollback,
                      level: SeverityLevel.warning,
                    ),
                  ],
                  if (deployment.initial) ...[
                    const SizedBox(width: 6),
                    Tag(label: l.deploymentsFirst, level: SeverityLevel.info),
                  ],
                ],
              ),
              const SizedBox(height: 2),
              Wrap(
                spacing: 10,
                children: [
                  Text(relativeTimeOf(l, deployment.timestamp), style: muted),
                  if (deployment.environment.isNotEmpty)
                    Text(deployment.environment, style: muted),
                ],
              ),
              if (busy)
                const Padding(
                  padding: EdgeInsets.symmetric(vertical: 12),
                  child: Center(
                    child: SizedBox(
                      width: 18,
                      height: 18,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    ),
                  ),
                ),
              if (open && compare != null) _Compare(compare: compare!),
            ],
          ),
        ),
      ),
    );
  }
}

class _Compare extends StatelessWidget {
  const _Compare({required this.compare});

  final ApmDeploymentCompare compare;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final before = compare.before;
    final after = compare.after;

    return Padding(
      padding: const EdgeInsets.only(top: 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            l.deploymentsWindow(compare.windowSeconds ~/ 60),
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
          const SizedBox(height: 6),
          _Delta(
            label: l.svcThroughput,
            before: before.throughput,
            after: after.throughput,
            format: _rpm,
            // More requests is not worse; only the error rate and the
            // latencies are bad when they go up.
            worseWhenUp: false,
          ),
          _Delta(
            label: l.serviceErrors,
            before: before.errorRate,
            after: after.errorRate,
            format: _rate,
            worseWhenUp: true,
          ),
          _Delta(
            label: 'p95',
            before: before.p95Ms,
            after: after.p95Ms,
            format: _ms,
            worseWhenUp: true,
          ),
          _Delta(
            label: l.svcApdex,
            before: before.apdex,
            after: after.apdex,
            format: _apdex,
            worseWhenUp: false,
          ),
          if (compare.newErrorGroups.isNotEmpty) ...[
            const SizedBox(height: 8),
            Text(
              l.deploymentsNewErrors(compare.newErrorGroups.length),
              style: theme.textTheme.bodySmall?.copyWith(
                color: severityTextColor(context, SeverityLevel.critical),
              ),
            ),
            for (final g in compare.newErrorGroups.take(5))
              Text(
                '${g.errorType}: ${g.message}',
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: theme.textTheme.bodySmall,
              ),
          ],
        ],
      ),
    );
  }
}

/// One number before and after, with the change between them.
class _Delta extends StatelessWidget {
  const _Delta({
    required this.label,
    required this.before,
    required this.after,
    required this.format,
    required this.worseWhenUp,
  });

  final String label;
  final double? before;
  final double? after;
  final String Function(double?) format;
  final bool worseWhenUp;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final ratio = deltaRatio(before, after);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    // No comparison is its own answer: a missing before, a missing after or
    // a zero before cannot produce a percentage that means anything.
    final color = ratio == null || ratio.abs() < 0.01
        ? null
        : severityTextColor(
            context,
            (ratio > 0) == worseWhenUp
                ? SeverityLevel.critical
                : SeverityLevel.good,
          );

    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 1),
      child: Row(
        children: [
          Expanded(child: Text(label, style: muted)),
          Text(format(before), style: muted),
          const _Arrow(),
          Text(format(after), style: muted),
          const SizedBox(width: 8),
          SizedBox(
            width: 64,
            child: Text(
              ratio == null ? '—' : _signed(ratio),
              textAlign: TextAlign.right,
              style: theme.textTheme.bodySmall?.copyWith(color: color),
            ),
          ),
        ],
      ),
    );
  }
}

/// The web draws the change with an arrow icon; so does this.
class _Arrow extends StatelessWidget {
  const _Arrow();

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.symmetric(horizontal: 4),
    child: Icon(
      Icons.arrow_right_alt,
      size: 16,
      color: Theme.of(context).colorScheme.onSurfaceVariant,
    ),
  );
}

/// `1.4.2` reads as a version with the web's `v` in front of it.
String _version(String v) {
  if (v.isEmpty) return '–';
  return RegExp(r'^v\d', caseSensitive: false).hasMatch(v) ? v : 'v$v';
}

String _signed(double ratio) {
  final percent = ratio * 100;
  final shown = percent.abs() >= 10
      ? percent.toStringAsFixed(0)
      : percent.toStringAsFixed(1);
  return percent > 0 ? '+$shown%' : '$shown%';
}

String _rpm(double? v) => v == null ? '—' : v.toStringAsFixed(v >= 100 ? 0 : 1);

String _rate(double? v) {
  if (v == null) return '—';
  final percent = v * 100;
  return '%${percent >= 10 ? percent.toStringAsFixed(0) : percent.toStringAsFixed(1)}';
}

String _ms(double? v) =>
    v == null ? '—' : '${v.toStringAsFixed(v >= 100 ? 0 : 1)} ms';

String _apdex(double? v) => v == null ? '—' : v.toStringAsFixed(2);
