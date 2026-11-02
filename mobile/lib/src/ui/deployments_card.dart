// When each version of a service first appeared, and what one of them did.
//
// The web puts this on the service's overview as a table with a comparison
// panel. Here it is a list of rows, and tapping one opens the same
// comparison underneath it: RED and Apdex before against after, plus the
// error groups first seen since.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../apm_format.dart';
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
    this.onOpenError,
  });

  final SessionController session;
  final ServiceDeploymentsController controller;

  /// Opens an error group a deployment brought, by its id.
  final ValueChanged<String>? onOpenError;

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
                // The deployment's own namespace and environment, exactly,
                // as the web compares it: a blank one is a value too.
                onTap: () => controller.toggleCompare(
                  d.t,
                  namespace: d.serviceNamespace,
                  environment: d.environment,
                ),
                window: controller.compareWindow,
                onWindow: controller.setCompareWindow,
                onOpenError: onOpenError,
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
    required this.window,
    required this.onWindow,
    this.onOpenError,
  });

  final ApmDeployment deployment;
  final bool open;
  final bool busy;
  final ApmDeploymentCompare? compare;
  final VoidCallback onTap;
  final String window;
  final ValueChanged<String> onWindow;
  final ValueChanged<String>? onOpenError;

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
              if (open) _WindowPicker(window: window, onChanged: onWindow),
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
              if (open && compare != null)
                _Compare(compare: compare!, onOpenError: onOpenError),
            ],
          ),
        ),
      ),
    );
  }
}

/// How far either side of the deployment to look.
class _WindowPicker extends StatelessWidget {
  const _WindowPicker({required this.window, required this.onChanged});

  final String window;
  final ValueChanged<String> onChanged;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.only(top: 8),
      child: Row(
        children: [
          Text(
            l.deploymentsWindowLabel,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
          const SizedBox(width: 10),
          DropdownButton<String>(
            key: const Key('deployment-window'),
            value: window,
            isDense: true,
            underline: const SizedBox.shrink(),
            items: [
              for (final w in compareWindows)
                DropdownMenuItem(
                  value: w,
                  child: Text(switch (w) {
                    '15m' => l.deploymentsWindow15m,
                    '1h' => l.deploymentsWindow1h,
                    '3h' => l.deploymentsWindow3h,
                    _ => l.deploymentsWindow30m,
                  }),
                ),
            ],
            onChanged: (w) {
              if (w != null && w != window) onChanged(w);
            },
          ),
        ],
      ),
    );
  }
}

class _Compare extends StatelessWidget {
  const _Compare({required this.compare, this.onOpenError});

  final ApmDeploymentCompare compare;
  final ValueChanged<String>? onOpenError;

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
          // The web's six, in its order.
          _Delta(
            metric: 'throughput',
            label: l.apmSortThroughput,
            before: before.throughput,
            after: after.throughput,
          ),
          _Delta(
            metric: 'error_rate',
            label: l.apmSortErrorRate,
            before: before.errorRate,
            after: after.errorRate,
          ),
          _Delta(
            metric: 'avg_ms',
            label: l.dbAvg,
            before: before.avgMs,
            after: after.avgMs,
          ),
          _Delta(
            metric: 'p95_ms',
            label: 'p95',
            before: before.p95Ms,
            after: after.p95Ms,
          ),
          _Delta(
            metric: 'p99_ms',
            label: 'p99',
            before: before.p99Ms,
            after: after.p99Ms,
          ),
          _Delta(
            metric: 'apdex',
            label: l.svcApdex,
            before: before.apdex,
            after: after.apdex,
          ),
          const SizedBox(height: 8),
          if (compare.newErrorGroups.isEmpty)
            Text(
              l.deploymentsNoNewErrors,
              key: const Key('deployment-no-new-errors'),
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            )
          else ...[
            Text(
              l.deploymentsNewErrors(compare.newErrorGroups.length),
              style: theme.textTheme.bodySmall?.copyWith(
                color: severityTextColor(context, SeverityLevel.critical),
              ),
            ),
            for (final g in compare.newErrorGroups)
              InkWell(
                key: Key('deployment-new-error-${g.groupId}'),
                onTap: onOpenError == null
                    ? null
                    : () => onOpenError!(g.groupId),
                child: Padding(
                  padding: const EdgeInsets.symmetric(vertical: 3),
                  child: Text(
                    '${g.errorType}: ${g.message}',
                    maxLines: 2,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.bodySmall,
                  ),
                ),
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
    required this.metric,
    required this.label,
    required this.before,
    required this.after,
  });

  final String metric;
  final String label;
  final double? before;
  final double? after;

  String _format(double? v) => switch (metric) {
    'throughput' => formatApmRpm(v),
    'error_rate' => formatApmRate(v),
    'apdex' => formatApmApdex(v),
    _ => formatApmMs(v),
  };

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final d = compareDelta(metric, before, after);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final color = switch (d.verdict) {
      'good' => severityTextColor(context, SeverityLevel.good),
      'bad' => severityTextColor(context, SeverityLevel.critical),
      _ => null,
    };

    return Padding(
      key: Key('compare-$metric'),
      padding: const EdgeInsets.symmetric(vertical: 2),
      // Two lines: "1.2 s > 1.4 s" and a change with its percentage do not fit
      // on one at this width, and which of them to cut is not obvious.
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(child: Text(label, style: muted)),
              Text(
                _change(d),
                key: Key('compare-$metric-change'),
                style: theme.textTheme.bodySmall?.copyWith(color: color),
              ),
            ],
          ),
          Row(
            children: [
              Text(_format(before), style: muted),
              const _Arrow(),
              Text(_format(after), style: muted),
            ],
          ),
        ],
      ),
    );
  }

  /// The change as the web writes it: the sign, the amount, and how much that
  /// is relative to before except for what is already a share of something.
  /// No comparison is its own answer, a dash, and never a zero.
  String _change(CompareDelta d) {
    final delta = d.delta;
    if (delta == null) return '–';
    final ratio = d.ratio;
    final percent = ratio == null || metric == 'error_rate' || metric == 'apdex'
        ? ''
        : ' (${ratio > 0
              ? '+'
              : ratio < 0
              ? '−'
              : ''}${formatApmRate(ratio.abs())})';
    final sign = switch (d.direction) {
      'up' => '+',
      'down' => '−',
      _ => '',
    };
    return '$sign${_format(delta.abs())}$percent';
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
