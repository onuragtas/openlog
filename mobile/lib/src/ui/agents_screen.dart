// Which language agent each service runs, and how far behind it is.
//
// Reached from the services list, as on the web. The upgrade commands are
// not here: they are a line to paste into a terminal and a phone has none,
// and asking for them makes the server check package registries for every
// service on the list.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../agents.dart';
import '../api/schema.g.dart';
import '../session.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'severity.dart';

class AgentsScreen extends StatefulWidget {
  const AgentsScreen({super.key, required this.session, required this.agents});

  final SessionController session;
  final ApmAgentsController agents;

  @override
  State<AgentsScreen> createState() => _AgentsScreenState();
}

class _AgentsScreenState extends State<AgentsScreen> {
  final _search = TextEditingController();

  @override
  void initState() {
    super.initState();
    final c = widget.agents;
    if (!c.loaded && !c.loadingFirst) {
      WidgetsBinding.instance.addPostFrameCallback((_) => c.refresh());
    }
  }

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.agents;

    return Scaffold(
      appBar: AppBar(
        title: Text(l.agentsTitle),
        actions: [
          IconButton(
            key: const Key('agents-refresh'),
            tooltip: l.refresh,
            onPressed: c.refresh,
            icon: const Icon(Icons.refresh),
          ),
        ],
      ),
      body: ListenableBuilder(
        listenable: c,
        builder: (context, _) => Column(
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(12, 10, 12, 0),
              child: TextField(
                key: const Key('agents-search'),
                controller: _search,
                decoration: InputDecoration(
                  hintText: l.agentsSearch,
                  prefixIcon: const Icon(Icons.search),
                  border: const OutlineInputBorder(),
                  isDense: true,
                ),
                // Filtered from what is already loaded: the endpoint answers
                // with every service at once and has no search of its own,
                // so a request per keystroke would re-read the same rows.
                onChanged: (v) {
                  c.query = v.trim();
                  c.refilter();
                },
              ),
            ),
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 4, 16, 0),
              child: Row(
                children: [
                  Expanded(child: _ReleaseLine(release: c.release)),
                  Text(l.agentsOutdatedOnly, style: theme.textTheme.bodySmall),
                  Switch(
                    key: const Key('agents-outdated'),
                    value: c.attentionOnly,
                    onChanged: (v) {
                      c.attentionOnly = v;
                      c.refilter();
                    },
                  ),
                ],
              ),
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
                        l.agentsEmpty,
                        key: const Key('agents-empty'),
                        style: theme.textTheme.bodyMedium?.copyWith(
                          color: theme.colorScheme.onSurfaceVariant,
                        ),
                      ),
                    )
                  : RefreshIndicator(
                      onRefresh: c.refresh,
                      child: ListView.separated(
                        padding: const EdgeInsets.fromLTRB(16, 8, 16, 28),
                        itemCount: c.items.length,
                        separatorBuilder: (context, _) =>
                            const Divider(height: 1),
                        itemBuilder: (context, i) => _AgentRow(row: c.items[i]),
                      ),
                    ),
            ),
          ],
        ),
      ),
    );
  }
}

/// What the versions are being compared with.
class _ReleaseLine extends StatelessWidget {
  const _ReleaseLine({required this.release});

  final ApmAgentRelease? release;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final r = release;
    if (r == null) return const SizedBox.shrink();
    // Without a catalog every openlog agent's status is `unknown`, so the
    // line says that instead of a version nothing was compared with.
    final text = r.catalog == ApmAgentReleaseCatalog.ok && r.latest != null
        ? l.agentsLatest(r.latest!, r.channel.wire)
        : l.agentsNoCatalog;
    return Text(
      text,
      key: const Key('agents-release'),
      style: theme.textTheme.bodySmall?.copyWith(
        color: theme.colorScheme.onSurfaceVariant,
      ),
    );
  }
}

class _AgentRow extends StatelessWidget {
  const _AgentRow({required this.row});

  final AgentRow row;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final (label, level) = switch (row.status) {
      ApmAgentStatus.ok => (l.agentsOk, SeverityLevel.good),
      ApmAgentStatus.outdated => (l.agentsOutdated, SeverityLevel.warning),
      ApmAgentStatus.unsupported => (
        l.agentsUnsupported,
        SeverityLevel.critical,
      ),
      ApmAgentStatus.thirdParty => (l.agentsThirdParty, SeverityLevel.info),
      // Including a status this build has never heard of: the server may
      // add one, and an unreadable word is better than a crash.
      ApmAgentStatus.unknown || ApmAgentStatus.unknownToThisBuild => (
        l.agentsUnknown,
        SeverityLevel.unknown,
      ),
    };

    return Padding(
      key: Key('agent-${row.serviceName}-${row.product}-${row.version}'),
      padding: const EdgeInsets.symmetric(vertical: 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  row.serviceName,
                  maxLines: 1,
                  overflow: TextOverflow.ellipsis,
                  style: theme.textTheme.titleSmall,
                ),
              ),
              Tag(label: label, level: level),
            ],
          ),
          const SizedBox(height: 2),
          Wrap(
            spacing: 8,
            runSpacing: 2,
            children: [
              Text(row.product, style: muted),
              Text(row.version, style: theme.textTheme.bodySmall),
              if (row.environment.isNotEmpty)
                Text(row.environment, style: muted),
              Text(l.agentsInstances(row.instances), style: muted),
              Text(relativeTimeOf(l, row.lastSeen), style: muted),
            ],
          ),
        ],
      ),
    );
  }
}
