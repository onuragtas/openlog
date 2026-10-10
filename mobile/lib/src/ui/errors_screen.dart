// The error inbox of every service.
//
// Reached from the services list, which is where the web keeps it too: it is
// not a section of its own there, and a drawer entry the web does not have
// would be a different app wearing the same words.
//
// The service pages have their own inbox; this is the one that answers "what
// is broken right now" without knowing which service to look at first.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../errors.dart';
import '../sections.dart';
import '../session.dart';
import 'error_group_screen.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'severity.dart';
import 'sparkline.dart';

class ErrorsScreen extends StatefulWidget {
  const ErrorsScreen({
    super.key,
    required this.session,
    required this.sections,
  });

  final SessionController session;
  final Sections sections;

  @override
  State<ErrorsScreen> createState() => _ErrorsScreenState();
}

class _ErrorsScreenState extends State<ErrorsScreen> {
  final _search = TextEditingController();

  @override
  void initState() {
    super.initState();
    final c = widget.sections.errors;
    if (!c.loaded && !c.loadingFirst) {
      WidgetsBinding.instance.addPostFrameCallback((_) => c.refresh());
    }
  }

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  String _statusLabel(L l, String s) => switch (s) {
    'unresolved' => l.errorsUnresolved,
    'resolved' => l.errorsResolved,
    'ignored' => l.errorsIgnored,
    _ => l.errorsAll,
  };

  int? _count(ErrorInboxController c, String s) {
    final counts = c.counts;
    if (counts == null) return null;
    return switch (s) {
      'unresolved' => counts.unresolved,
      'resolved' => counts.resolved,
      'ignored' => counts.ignored,
      _ => counts.unresolved + counts.resolved + counts.ignored,
    };
  }

  String _sortLabel(L l, String s) => switch (s) {
    'last_seen' => l.errorsSortLastSeen,
    'first_seen' => l.errorsSortFirstSeen,
    _ => l.errorsSortCount,
  };

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.sections.errors;

    return Scaffold(
      appBar: AppBar(
        title: Text(l.navErrors),
        actions: [
          IconButton(
            key: const Key('errors-refresh'),
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
              child: Row(
                children: [
                  Expanded(
                    child: TextField(
                      key: const Key('errors-search'),
                      controller: _search,
                      textInputAction: TextInputAction.search,
                      decoration: InputDecoration(
                        hintText: l.errorsSearch,
                        prefixIcon: const Icon(Icons.search),
                        border: const OutlineInputBorder(),
                        isDense: true,
                      ),
                      onSubmitted: (v) {
                        c.query = v.trim();
                        c.refresh();
                      },
                    ),
                  ),
                  PopupMenuButton<String>(
                    key: const Key('errors-sort'),
                    tooltip: l.errorsSort,
                    icon: const Icon(Icons.swap_vert),
                    onSelected: (s) {
                      if (c.sort == s) return;
                      c.sort = s;
                      c.refresh();
                    },
                    itemBuilder: (context) => [
                      for (final s in errorSorts)
                        PopupMenuItem(
                          value: s,
                          child: Text(
                            _sortLabel(l, s),
                            style: s == c.sort
                                ? TextStyle(color: theme.colorScheme.primary)
                                : null,
                          ),
                        ),
                    ],
                  ),
                ],
              ),
            ),
            if (c.workflow)
              SingleChildScrollView(
                scrollDirection: Axis.horizontal,
                padding: const EdgeInsets.fromLTRB(12, 8, 12, 2),
                child: Row(
                  children: [
                    for (final s in errorStatuses)
                      Padding(
                        padding: const EdgeInsets.only(right: 8),
                        child: ChoiceChip(
                          key: Key('errors-status-$s'),
                          label: Text(switch (_count(c, s)) {
                            final n? => '${_statusLabel(l, s)} $n',
                            _ => _statusLabel(l, s),
                          }),
                          selected: c.status == s,
                          onSelected: (_) {
                            if (c.status == s) return;
                            c.status = s;
                            c.refresh();
                          },
                        ),
                      ),
                  ],
                ),
              )
            else
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 8, 16, 0),
                child: Text(
                  // No PostgreSQL: there is no workflow at all and every group
                  // reads as unresolved. Saying so beats four tabs that do
                  // nothing.
                  l.errorsNoWorkflow,
                  key: const Key('errors-no-workflow'),
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
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
                        l.errorsEmpty,
                        key: const Key('errors-empty'),
                        style: theme.textTheme.bodyMedium?.copyWith(
                          color: theme.colorScheme.onSurfaceVariant,
                        ),
                      ),
                    )
                  : RefreshIndicator(
                      onRefresh: c.refresh,
                      child: ListView.builder(
                        padding: const EdgeInsets.fromLTRB(12, 6, 12, 28),
                        itemCount: c.items.length + (c.truncated ? 1 : 0),
                        itemBuilder: (context, i) {
                          if (i == c.items.length) {
                            return Padding(
                              padding: const EdgeInsets.all(12),
                              child: Text(
                                l.errorsTruncated,
                                style: theme.textTheme.bodySmall?.copyWith(
                                  color: theme.colorScheme.onSurfaceVariant,
                                ),
                              ),
                            );
                          }
                          final g = c.items[i];
                          return ErrorGroupCard(
                            group: g,
                            onOpen: () => Navigator.of(context).push(
                              MaterialPageRoute<void>(
                                builder: (_) => ErrorGroupScreen(
                                  session: widget.session,
                                  sections: widget.sections,
                                  group: g,
                                ),
                              ),
                            ),
                          );
                        },
                      ),
                    ),
            ),
          ],
        ),
      ),
    );
  }
}

/// One group, as a row.
class ErrorGroupCard extends StatelessWidget {
  const ErrorGroupCard({super.key, required this.group, required this.onOpen});

  final ApmErrorGroup group;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final spark = <double>[
      for (final p in group.sparkline)
        if (p.length > 1) p[1],
    ];

    return Card(
      key: Key('error-${group.groupId}'),
      margin: const EdgeInsets.symmetric(vertical: 5),
      child: InkWell(
        onTap: onOpen,
        borderRadius: BorderRadius.circular(12),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(14, 12, 14, 12),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                group.errorType,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: theme.textTheme.titleSmall,
              ),
              Text(
                group.message,
                maxLines: 2,
                overflow: TextOverflow.ellipsis,
                style: theme.textTheme.bodySmall,
              ),
              const SizedBox(height: 6),
              Wrap(
                spacing: 8,
                runSpacing: 4,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  Text(group.serviceName, style: muted),
                  Text(l.errorsCount(group.count.round()), style: muted),
                  if (group.lastSeen != null)
                    Text(relativeTimeOf(l, group.lastSeen!), style: muted),
                  ...errorStatusTags(l, group),
                  if (group.commentCount > 0) ...[
                    // An icon rather than an emoji: the app draws everything
                    // else with Material icons, and an emoji renders as a
                    // box wherever the font has none.
                    Icon(
                      Icons.chat_bubble_outline,
                      size: 13,
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                    Text('${group.commentCount}', style: muted),
                  ],
                ],
              ),
              if (spark.length > 1)
                Padding(
                  padding: const EdgeInsets.only(top: 8),
                  child: SizedBox(
                    height: 26,
                    child: Sparkline(
                      values: spark,
                      color: theme.colorScheme.primary,
                    ),
                  ),
                ),
            ],
          ),
        ),
      ),
    );
  }
}

/// What has been decided about a group, as tags.
List<Widget> errorStatusTags(L l, ApmErrorGroup g) => [
  // Regressed first: "somebody fixed this and it came back" is a different
  // thing from "nobody has looked yet", and the row has to say which.
  if (isRegressed(g))
    Tag(label: l.errorsRegressed, level: SeverityLevel.critical)
  else if (g.status == ApmErrorStatus.resolved)
    Tag(label: l.errorsResolved, level: SeverityLevel.good)
  else if (g.status == ApmErrorStatus.ignored)
    Tag(label: l.errorsIgnored, level: SeverityLevel.unknown),
  if (g.assignee != null)
    Tag(
      label: g.assignee!.name.isEmpty ? g.assignee!.email : g.assignee!.name,
      level: SeverityLevel.info,
    ),
];
