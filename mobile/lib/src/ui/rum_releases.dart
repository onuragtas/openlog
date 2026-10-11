// How each version of a browser application behaved, and a funnel over the
// application's own custom events (rum.md §2.7, §2.8).
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../rum.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'oql_view.dart' show formatNumber;
import 'severity.dart';
import 'theme.dart';

class RumReleasesTab extends StatefulWidget {
  const RumReleasesTab({
    super.key,
    required this.session,
    required this.sections,
    required this.app,
  });

  final SessionController session;
  final Sections sections;
  final String app;

  @override
  State<RumReleasesTab> createState() => _RumReleasesTabState();
}

class _RumReleasesTabState extends State<RumReleasesTab> {
  late final RumReleasesController _c = widget.sections.rumReleases(widget.app);

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
      builder: (context, _) => DetailBody<RumReleaseList>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        builder: (context, v) => _body(context, l, v),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, RumReleaseList v) {
    final theme = Theme.of(context);
    if (v.releases.isEmpty && v.deployments.isEmpty) {
      return [
        const SizedBox(height: 48),
        Text(
          l.rumReleasesEmpty,
          textAlign: TextAlign.center,
          style: theme.textTheme.titleMedium,
        ),
      ];
    }
    return [
      DetailSection(
        title: l.rumReleaseVersions,
        children: [
          // The two halves of this screen see different distances, and a
          // version with no sessions is a gap in what is kept rather than a
          // quiet week.
          Text(
            l.rumReleaseRetention,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
          const SizedBox(height: 10),
          for (final r in v.releases)
            _ReleaseRow(release: r, key: Key('rum-release-${r.version}')),
        ],
      ),
      DetailSection(
        title: l.rumReleaseDeployments,
        children: [
          for (final d in v.deployments)
            _DeploymentRow(deployment: d, key: Key('rum-deploy-${d.t}')),
        ],
      ),
    ];
  }
}

class _ReleaseRow extends StatelessWidget {
  const _ReleaseRow({super.key, required this.release});

  final RumRelease release;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final measured = release.sessions > 0;
    // Below 99% is worth a colour, below 95% the loud one. The bands are this
    // screen's own: nothing in the contract says what a good release is.
    final level = !measured
        ? SeverityLevel.unknown
        : release.crashFreeRate >= 0.99
        ? SeverityLevel.good
        : release.crashFreeRate >= 0.95
        ? SeverityLevel.warning
        : SeverityLevel.critical;

    return Padding(
      padding: const EdgeInsets.only(bottom: 10),
      child: Row(
        children: [
          Expanded(
            child: Text(
              release.version.isEmpty ? l.rumReleaseNoVersion : release.version,
              style: theme.textTheme.bodyMedium?.copyWith(
                fontWeight: FontWeight.w600,
              ),
            ),
          ),
          Text(
            measured
                ? '${formatNumber(release.sessions.toDouble())} · '
                      '${formatNumber(release.errorSessions.toDouble())}'
                : l.rumReleaseNoSessions,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
          const SizedBox(width: 10),
          Tag(
            label: measured
                ? '${(release.crashFreeRate * 100).toStringAsFixed(1)}%'
                : '-',
            level: level,
          ),
        ],
      ),
    );
  }
}

class _DeploymentRow extends StatelessWidget {
  const _DeploymentRow({super.key, required this.deployment});

  final ApmDeployment deployment;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: Wrap(
        spacing: 8,
        runSpacing: 4,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          Text(deployment.version, style: theme.textTheme.bodyMedium),
          if (deployment.previousVersion.isNotEmpty)
            Text(
              '← ${deployment.previousVersion}',
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          if (deployment.initial)
            Tag(label: l.rumReleaseInitial, level: SeverityLevel.unknown),
          // A version that came back is a rollback, and that is the first
          // thing to know about it.
          if (deployment.rollback)
            Tag(label: l.rumReleaseRollback, level: SeverityLevel.warning),
          Text(
            relativeTimeOf(l, deployment.timestamp),
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
        ],
      ),
    );
  }
}

/// How far the sessions that did the first step got, in order.
class RumFunnelTab extends StatefulWidget {
  const RumFunnelTab({
    super.key,
    required this.session,
    required this.sections,
    required this.app,
  });

  final SessionController session;
  final Sections sections;
  final String app;

  @override
  State<RumFunnelTab> createState() => _RumFunnelTabState();
}

class _RumFunnelTabState extends State<RumFunnelTab> {
  late final RumFunnelController _c = widget.sections.rumFunnel(widget.app);
  final _steps = TextEditingController();

  /// The windows the web offers.
  static const _windows = ['5m', '15m', '30m', '1h', '6h', '24h'];

  @override
  void dispose() {
    _steps.dispose();
    _c.dispose();
    super.dispose();
  }

  void _apply() {
    final steps = [
      for (final s in _steps.text.split(','))
        if (s.trim().isNotEmpty) s.trim(),
    ];
    // Six is what the endpoint takes; a longer list is cut rather than sent
    // to be refused.
    _c.run(steps: steps.take(6).toList());
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return ListenableBuilder(
      listenable: _c,
      builder: (context, _) => ListView(
        padding: const EdgeInsets.fromLTRB(12, 12, 12, 24),
        children: [
          TextField(
            key: const Key('rum-funnel-steps'),
            controller: _steps,
            autocorrect: false,
            textInputAction: TextInputAction.search,
            decoration: InputDecoration(
              labelText: l.rumFunnelSteps,
              border: const OutlineInputBorder(),
              isDense: true,
            ),
            onSubmitted: (_) => _apply(),
          ),
          const SizedBox(height: 8),
          Row(
            children: [
              DropdownButton<String>(
                key: const Key('rum-funnel-window'),
                value: _c.window,
                underline: const SizedBox.shrink(),
                items: [
                  for (final w in _windows)
                    DropdownMenuItem(
                      value: w,
                      child: Text('${l.rumFunnelWindow} $w'),
                    ),
                ],
                onChanged: (v) {
                  if (v == null) return;
                  _c.run(window: v);
                },
              ),
              const Spacer(),
              FilledButton(
                key: const Key('rum-funnel-apply'),
                onPressed: _apply,
                child: Text(l.rumFunnelApply),
              ),
            ],
          ),
          const SizedBox(height: 6),
          Text(
            l.rumFunnelHint,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
          FailureBanner(
            failure: _c.failure,
            baseUrl: widget.session.baseUrl ?? '',
          ),
          const SizedBox(height: 12),
          if (!_c.ready)
            Text(l.rumFunnelNeedSteps, style: theme.textTheme.bodyMedium)
          else if (_c.loading && _c.value == null)
            const Center(child: CircularProgressIndicator())
          else if (_c.value != null)
            ..._steps2(context, l, _c.value!),
        ],
      ),
    );
  }

  List<Widget> _steps2(BuildContext context, L l, RumFunnelResponse v) {
    final theme = Theme.of(context);
    final first = v.steps.firstOrNull;
    return [
      for (var i = 0; i < v.steps.length; i++)
        _StepRow(
          step: v.steps[i],
          previous: i == 0 ? null : v.steps[i - 1],
          index: i,
          key: Key('rum-funnel-step-$i'),
        ),
      if (first != null && v.steps.isNotEmpty)
        Padding(
          padding: const EdgeInsets.only(top: 8),
          child: Text(
            l.rumFunnelCompletion(
              (v.steps.last.rate * 100).round(),
              formatNumber(first.sessions.toDouble()),
              _c.window,
            ),
            style: theme.textTheme.bodyMedium,
          ),
        ),
    ];
  }
}

class _StepRow extends StatelessWidget {
  const _StepRow({
    super.key,
    required this.step,
    required this.previous,
    required this.index,
  });

  final RumFunnelStep step;
  final RumFunnelStep? previous;
  final int index;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    // The drop that matters is the one against the step before, not against
    // the start: a funnel that loses 90% at step two is a different problem
    // from one that leaks everywhere.
    final prev = previous;
    final dropped = prev != null && prev.sessions > 0
        ? 1 - step.sessions / prev.sessions
        : 0.0;

    return Padding(
      padding: const EdgeInsets.only(bottom: 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Text(
                '${index + 1}.',
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
              const SizedBox(width: 6),
              Expanded(
                child: Text(
                  step.step,
                  style: theme.textTheme.bodyMedium,
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis,
                ),
              ),
              Text(
                '%${(step.rate * 100).round()}',
                style: theme.textTheme.titleSmall?.copyWith(
                  fontWeight: FontWeight.w600,
                ),
              ),
            ],
          ),
          const SizedBox(height: 4),
          SizedBox(
            height: 8,
            child: ClipRRect(
              borderRadius: BorderRadius.circular(4),
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  Expanded(
                    flex: (step.rate.clamp(0, 1) * 1000).round(),
                    child: ColoredBox(color: colors.primary),
                  ),
                  Expanded(
                    flex: ((1 - step.rate.clamp(0, 1)) * 1000).round(),
                    child: ColoredBox(color: colors.muted),
                  ),
                ],
              ),
            ),
          ),
          const SizedBox(height: 4),
          Text(
            [
              l.rumFunnelSessions(formatNumber(step.sessions.toDouble())),
              if (dropped > 0) l.rumFunnelDropped((dropped * 100).round()),
            ].join(' · '),
            style: theme.textTheme.bodySmall?.copyWith(
              color: dropped > 0
                  ? severityTextColor(context, SeverityLevel.critical)
                  : theme.colorScheme.onSurfaceVariant,
            ),
          ),
        ],
      ),
    );
  }
}
