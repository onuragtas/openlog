// Containers: the web's list, its filters and its grouped view.
//
// The web has a search, a host, a compose project, a state and a button
// that groups the rows by compose service. All five are here; on a phone
// the selects are chips that open a searchable sheet, because a host list
// is as long as the fleet.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../session.dart';
import 'container_screen.dart' hide formatBytes;
import '../usage.dart' show formatBytes;
import 'list_scaffold.dart';
import 'pick_sheet.dart';
import 'sections_screen.dart';

class ContainersBody extends StatefulWidget {
  const ContainersBody({
    super.key,
    required this.session,
    required this.sections,
    required this.active,
  });

  final SessionController session;
  final Sections sections;
  final bool active;

  @override
  State<ContainersBody> createState() => _ContainersBodyState();
}

class _ContainersBodyState extends State<ContainersBody> {
  final _search = TextEditingController();

  @override
  void initState() {
    super.initState();
    _loadIfVisible();
  }

  @override
  void didUpdateWidget(ContainersBody old) {
    super.didUpdateWidget(old);
    _loadIfVisible();
  }

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  void _loadIfVisible() {
    final c = widget.sections.containers;
    if (!widget.active || c.loaded || c.loadingFirst) return;
    WidgetsBinding.instance.addPostFrameCallback((_) => c.refresh());
  }

  /// The hosts to filter by. They are a section of their own, so they may
  /// not have been asked for yet when somebody opens this filter.
  Future<List<Host>> _hosts() async {
    final hosts = widget.sections.hosts;
    if (!hosts.loaded && !hosts.loadingFirst) await hosts.refresh();
    return hosts.items;
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.sections.containers;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        final groups = c.grouped ? groupByComposeService(c.items) : null;
        return ListScreen<ApiContainer>(
          controller: c,
          baseUrl: widget.session.baseUrl ?? '',
          search: SearchField(
            fieldKey: const Key('containers-search'),
            controller: _search,
            hint: l.sectionSearch,
            onSubmitted: (v) {
              c.query = v;
              c.refresh();
            },
          ),
          header: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Wrap(
                spacing: 6,
                runSpacing: 4,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  _Chip(
                    chipKey: const Key('containers-host'),
                    label: l.containersHost,
                    // The chip says the host's name; the filter sends its
                    // id, which is not a thing anybody recognises.
                    value: _hostName(c.hostId),
                    onTap: () async {
                      final hosts = await _hosts();
                      if (!context.mounted) return;
                      final byName = {
                        for (final h in hosts)
                          (h.hostName.isEmpty ? h.hostId : h.hostName):
                              h.hostId,
                      };
                      final picked = await pickOne(
                        context,
                        title: l.containersHost,
                        options: byName.keys.toList(),
                        firstLabel: l.containersAllHosts,
                      );
                      if (picked == null) return;
                      c.hostId = picked.isEmpty ? '' : byName[picked] ?? '';
                      c.refresh();
                    },
                    onClear: c.hostId.isEmpty
                        ? null
                        : () {
                            c.hostId = '';
                            c.refresh();
                          },
                  ),
                  _Chip(
                    chipKey: const Key('containers-project'),
                    label: l.containersProject,
                    value: c.composeProject == null
                        ? ''
                        : c.composeProject!.isEmpty
                        ? l.containersNoProject
                        : c.composeProject!,
                    onTap: () async {
                      final projects = [
                        for (final p in c.projects)
                          if (p.composeProject.isNotEmpty) p.composeProject,
                        l.containersNoProject,
                      ];
                      final picked = await pickOne(
                        context,
                        title: l.containersProject,
                        options: projects,
                        firstLabel: l.containersAllProjects,
                      );
                      if (picked == null) return;
                      c.composeProject = picked.isEmpty
                          ? null
                          : picked == l.containersNoProject
                          ? ''
                          : picked;
                      c.refresh();
                    },
                    onClear: c.composeProject == null
                        ? null
                        : () {
                            c.composeProject = null;
                            c.refresh();
                          },
                  ),
                  _Chip(
                    chipKey: const Key('containers-state'),
                    label: l.containersState,
                    value: c.state,
                    onTap: () async {
                      final picked = await pickOne(
                        context,
                        title: l.containersState,
                        options: containerStates,
                        firstLabel: l.containersAllStates,
                      );
                      if (picked == null) return;
                      c.state = picked;
                      c.refresh();
                    },
                    onClear: c.state.isEmpty
                        ? null
                        : () {
                            c.state = '';
                            c.refresh();
                          },
                  ),
                  FilterChip(
                    key: const Key('containers-group'),
                    avatar: const Icon(Icons.layers_outlined, size: 16),
                    label: Text(l.containersGroup),
                    selected: c.grouped,
                    // Grouping is a way of reading the same rows, not a
                    // different question, so nothing is asked again.
                    onSelected: (v) => setState(() => c.grouped = v),
                  ),
                ],
              ),
              if (c.total > c.items.length)
                Padding(
                  padding: const EdgeInsets.only(top: 6),
                  child: Text(
                    l.containersShown(c.items.length, c.total),
                    key: const Key('containers-shown'),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                ),
            ],
          ),
          emptyTitle: l.containersEmpty,
          // Grouped, the list is one row per group with its containers
          // under it; the rows themselves are the same cards.
          itemCount: groups?.length,
          itemBuilder: (context, i) => groups == null
              ? _card(context, c.items[i])
              : _Group(group: groups[i], card: (x) => _card(context, x)),
        );
      },
    );
  }

  String _hostName(String hostId) {
    if (hostId.isEmpty) return '';
    for (final h in widget.sections.hosts.items) {
      if (h.hostId == hostId) {
        return h.hostName.isEmpty ? h.hostId : h.hostName;
      }
    }
    return hostId;
  }

  Widget _card(
    BuildContext context,
    ApiContainer x, {
    bool showCompose = true,
  }) => containerCard(
    context,
    x,
    showCompose: showCompose,
    onOpen: () => Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => ContainerScreen(
          session: widget.session,
          sections: widget.sections,
          containerId: x.containerId,
          name: x.name.isEmpty ? x.containerId : x.name,
        ),
      ),
    ),
  );
}

/// One compose service: its name, how many of its containers are running
/// and what they are using, with the containers under it.
class _Group extends StatelessWidget {
  const _Group({required this.group, required this.card});

  final ContainerGroup group;
  final Widget Function(ApiContainer) card;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final g = group;

    return Column(
      key: Key('container-group-${g.project}/${g.service}'),
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(4, 10, 4, 2),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                g.standalone
                    ? l.containersUngrouped
                    : g.project.isEmpty
                    ? g.service
                    : '${g.service} · ${g.project}',
                style: theme.textTheme.titleSmall,
              ),
              Text(
                [
                  l.containersRunning(g.running, g.containers.length),
                  if (g.cpu != null) 'CPU %${(g.cpu! * 100).round()}',
                  if (g.memory != null) formatBytes(g.memory!),
                ].join(' · '),
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            ],
          ),
        ),
        for (final x in g.containers) card(x),
      ],
    );
  }
}

class _Chip extends StatelessWidget {
  const _Chip({
    required this.chipKey,
    required this.label,
    required this.value,
    required this.onTap,
    required this.onClear,
  });

  final Key chipKey;
  final String label;
  final String value;
  final VoidCallback onTap;
  final VoidCallback? onClear;

  @override
  Widget build(BuildContext context) => InputChip(
    key: chipKey,
    label: Text(value.isEmpty ? label : value),
    avatar: value.isEmpty
        ? const Icon(Icons.filter_alt_outlined, size: 16)
        : null,
    onDeleted: onClear,
    onPressed: onTap,
  );
}
