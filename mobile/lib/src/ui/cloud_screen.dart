// Cloud connections, read-only.
//
// The web reaches these from the integrations page, and so does this app:
// managed services have no agent to discover, so they are configured
// rather than found. The list and one connection's polls are here; the
// form that stores an access key is not -- that is the web's job, and the
// screen says so instead of pretending the button is missing.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../cloud.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'list_scaffold.dart';
import 'sections_screen.dart';
import 'severity.dart';

class CloudScreen extends StatefulWidget {
  const CloudScreen({super.key, required this.session, required this.sections});

  final SessionController session;
  final Sections sections;

  @override
  State<CloudScreen> createState() => _CloudScreenState();
}

class _CloudScreenState extends State<CloudScreen> {
  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback(
      (_) => widget.sections.cloud.refresh(),
    );
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.sections.cloud;

    return Scaffold(
      appBar: AppBar(title: Text(l.cloudTitle)),
      body: ListenableBuilder(
        listenable: c,
        builder: (context, _) => ListScreen<CloudConnection>(
          controller: c,
          baseUrl: widget.session.baseUrl ?? '',
          header: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              if (!c.secretsConfigured)
                Padding(
                  padding: const EdgeInsets.only(bottom: 8),
                  child: Text(
                    // No secrets key, no stored credentials, so there can
                    // be no connections at all.
                    l.cloudNoSecretsKey,
                    key: const Key('cloud-no-key'),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: severityTextColor(context, SeverityLevel.warning),
                    ),
                  ),
                ),
              Padding(
                padding: const EdgeInsets.only(bottom: 4),
                child: Text(
                  l.cloudManageOnWeb,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              ),
            ],
          ),
          emptyTitle: l.cloudEmpty,
          itemBuilder: (context, i) => _ConnectionCard(
            connection: c.items[i],
            onOpen: () => Navigator.of(context).push(
              MaterialPageRoute<void>(
                builder: (_) => CloudConnectionScreen(
                  session: widget.session,
                  sections: widget.sections,
                  connection: c.items[i],
                ),
              ),
            ),
          ),
        ),
      ),
    );
  }
}

SeverityLevel _level(String state) => switch (state) {
  'ok' => SeverityLevel.info,
  'partial' => SeverityLevel.warning,
  'error' => SeverityLevel.critical,
  _ => SeverityLevel.unknown,
};

String _stateLabel(L l, String state) => switch (state) {
  'ok' => l.cloudStateOk,
  'partial' => l.cloudStatePartial,
  'error' => l.cloudStateError,
  'paused' => l.cloudStatePaused,
  _ => l.cloudStateUnknown,
};

String _providerLabel(CloudProviderName p) => switch (p) {
  CloudProviderName.aws => 'AWS',
  CloudProviderName.azure => 'Azure',
  CloudProviderName.gcp => 'GCP',
  _ => p.wire,
};

String _scopeLabel(L l, CloudProviderName provider, int count) =>
    switch (cloudScopeKind(provider)) {
      'region' => l.cloudRegions(count),
      'subscription' => l.cloudSubscriptions(count),
      'project' => l.cloudProjects(count),
      _ => l.cloudScopes(count),
    };

class _ConnectionCard extends StatelessWidget {
  const _ConnectionCard({required this.connection, required this.onOpen});

  final CloudConnection connection;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = connection;
    final state = cloudState(c);
    final last = cloudLastRun(c.status);
    final error = cloudFirstError(c.status);

    return SectionCard(
      cardKey: Key('cloud-${c.id}'),
      onOpen: onOpen,
      title: c.name,
      subtitle: [
        _providerLabel(c.provider),
        _scopeLabel(l, c.provider, c.scopes.length),
        l.cloudServices(c.services.length),
      ].join(' · '),
      trailing: last == null ? l.cloudNever : relativeTimeOf(l, last),
      tags: [
        Tag(label: _stateLabel(l, state), level: _level(state)),
        if (!c.credentialsSet)
          Tag(label: l.cloudNoCredentials, level: SeverityLevel.warning),
      ],
      stats: [
        (
          label: l.cloudCollected,
          value: '${cloudLastMetrics(c.status)}',
          emphasis: null,
        ),
      ],
      // What is wrong, on the row: the web shows it here too, so a list of
      // five connections says which one to open.
      extra: error.isEmpty
          ? null
          : Text(
              error,
              maxLines: 2,
              overflow: TextOverflow.ellipsis,
              style: Theme.of(context).textTheme.bodySmall?.copyWith(
                color: severityTextColor(context, SeverityLevel.critical),
              ),
            ),
    );
  }
}

/// One connection: what each scope's last poll did, and the polls before
/// it.
class CloudConnectionScreen extends StatefulWidget {
  const CloudConnectionScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.connection,
  });

  final SessionController session;
  final Sections sections;
  final CloudConnection connection;

  @override
  State<CloudConnectionScreen> createState() => _CloudConnectionScreenState();
}

class _CloudConnectionScreenState extends State<CloudConnectionScreen> {
  late final CloudRunsController _c;

  @override
  void initState() {
    super.initState();
    _c = widget.sections.cloudRuns(widget.connection.id);
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
      builder: (context, _) => DetailScreen<CloudRunList>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: widget.connection.name,
        subtitle: _providerLabel(widget.connection.provider),
        builder: (context, page) => _body(context, l, page),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, CloudRunList page) {
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    // The connection as the server has it now, not as the list had it when
    // this screen was opened.
    final c = page.connection;
    final state = cloudState(c);

    return [
      Wrap(
        spacing: 8,
        runSpacing: 4,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          Tag(label: _stateLabel(l, state), level: _level(state)),
          Text(
            l.cloudEvery(Duration(seconds: c.pollIntervalSeconds).inMinutes),
            style: muted,
          ),
          Text(
            l.cloudCaps(c.maxMetricsPerPoll, c.maxApiCallsPerPoll),
            style: muted,
          ),
        ],
      ),
      if (c.services.isNotEmpty) ...[
        const SizedBox(height: 8),
        Wrap(
          spacing: 6,
          runSpacing: 4,
          children: [for (final s in c.services) Tag(label: s)],
        ),
      ],

      DetailSection(
        title: _scopeLabel(l, c.provider, c.status.length),
        children: [for (final s in c.status) _ScopeRow(status: s)],
      ),

      DetailSection(
        title: l.cloudRuns,
        children: [
          if (page.runs.isEmpty)
            Text(l.cloudNoRuns, key: const Key('cloud-no-runs'), style: muted)
          else
            for (final r in page.runs) _RunRow(run: r),
        ],
      ),
    ];
  }
}

class _ScopeRow extends StatelessWidget {
  const _ScopeRow({required this.status});

  final CloudScopeStatus status;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final s = status;
    final state = switch (s.lastStatus) {
      CloudScopeStatusLastStatus.ok => 'ok',
      CloudScopeStatusLastStatus.partial => 'partial',
      CloudScopeStatusLastStatus.error => 'error',
      _ => 'unknown',
    };

    return Padding(
      key: Key('cloud-scope-${s.scope}'),
      padding: const EdgeInsets.symmetric(vertical: 6),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(child: Text(s.scope, style: theme.textTheme.bodyMedium)),
              Tag(label: _stateLabel(l, state), level: _level(state)),
            ],
          ),
          Text(
            [
              s.lastRunAt == null
                  ? l.cloudNever
                  : relativeTimeOf(l, s.lastRunAt!),
              l.cloudCollectedN(s.lastMetrics),
              l.cloudApiCalls(s.lastApiCalls),
              // When the next poll is due; after a failure the server
              // backs off, so "in 40 minutes" is itself the symptom.
              l.cloudNext(relativeTimeOf(l, s.nextRunAt)),
            ].join(' · '),
            style: muted,
          ),
          if (s.consecutiveErrors > 0)
            Text(
              l.cloudConsecutiveErrors(s.consecutiveErrors),
              style: theme.textTheme.bodySmall?.copyWith(
                color: severityTextColor(context, SeverityLevel.critical),
              ),
            ),
          if (s.lastError.isNotEmpty)
            Text(
              s.lastError,
              style: theme.textTheme.bodySmall?.copyWith(
                color: severityTextColor(context, SeverityLevel.critical),
              ),
            ),
          const Divider(height: 14),
        ],
      ),
    );
  }
}

class _RunRow extends StatelessWidget {
  const _RunRow({required this.run});

  final CloudRun run;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final state = switch (run.status) {
      CloudRunStatus.ok => 'ok',
      CloudRunStatus.partial => 'partial',
      _ => 'error',
    };

    return Padding(
      key: Key('cloud-run-${run.id}'),
      padding: const EdgeInsets.symmetric(vertical: 5),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  '${relativeTimeOf(l, run.startedAt)} · ${run.scope}',
                  style: theme.textTheme.bodyMedium,
                ),
              ),
              Tag(label: _stateLabel(l, state), level: _level(state)),
            ],
          ),
          Text(
            [
              l.cloudCollectedN(run.metrics),
              l.cloudApiCalls(run.apiCalls),
              if (run.throttled > 0) l.cloudThrottled(run.throttled),
              '${(run.durationMs / 1000).toStringAsFixed(1)} s',
            ].join(' · '),
            style: muted,
          ),
          // What each service of this poll brought back, as the web
          // lists it: a poll that collected nothing from one service is
          // not a poll that collected nothing.
          if (run.services.isNotEmpty)
            Text(
              [
                for (final s in run.services) '${s.service}: ${s.metrics}',
              ].join(' · '),
              style: muted,
            ),
          if (run.error.isNotEmpty)
            Text(
              run.error,
              maxLines: 2,
              overflow: TextOverflow.ellipsis,
              style: theme.textTheme.bodySmall?.copyWith(
                color: severityTextColor(context, SeverityLevel.critical),
              ),
            ),
        ],
      ),
    );
  }
}
