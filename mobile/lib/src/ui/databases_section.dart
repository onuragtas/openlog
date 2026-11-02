// The Databases section: the same statements seen from two sides, the
// applications that run them and the servers that execute them.
//
// It opens on the servers, unless they answered and there are none: with no
// server reporting that list has nothing to say and the applications may well
// have. A choice the person made stays, whatever the next answer is.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../databases.dart';
import '../sections.dart';
import '../session.dart';
import 'app_db_queries.dart';
import 'database_screen.dart';
import 'list_paging.dart';
import 'list_scaffold.dart';
import 'sections_screen.dart';

/// The app bar's refresh: the servers, which decide where the section opens,
/// and the applications once somebody has looked at them.
Future<void> refreshDatabases(Sections s) async {
  await s.databases.refresh();
  if (s.appDbQueries.loaded) await s.appDbQueries.refresh();
}

class DatabasesBody extends StatefulWidget {
  const DatabasesBody({
    super.key,
    required this.session,
    required this.sections,
    required this.active,
  });

  final SessionController session;
  final Sections sections;

  /// Whether this section is the one on screen: nothing is asked before.
  final bool active;

  @override
  State<DatabasesBody> createState() => _DatabasesBodyState();
}

class _DatabasesBodyState extends State<DatabasesBody> {
  String? _chosen;

  DatabasesController get _servers => widget.sections.databases;
  AppDbQueriesController get _apps => widget.sections.appDbQueries;

  String get _source =>
      _chosen ??
      defaultDatabaseSource(
        loaded: _servers.loaded,
        failed: _servers.failure != null,
        count: _servers.items.length,
      );

  @override
  void initState() {
    super.initState();
    _servers.addListener(_loadApplications);
    _loadServers();
  }

  @override
  void didUpdateWidget(DatabasesBody old) {
    super.didUpdateWidget(old);
    _loadServers();
    // Also a chance to ask again after a failure, which the listener below
    // will not: it would ask forever.
    if (widget.active && _source == 'applications') _askApplications();
  }

  @override
  void dispose() {
    _servers.removeListener(_loadApplications);
    super.dispose();
  }

  void _loadServers() {
    if (!widget.active || _servers.loaded || _servers.loadingFirst) return;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) _servers.refresh();
    });
  }

  /// The section just decided to open on the applications.
  void _loadApplications() {
    if (!mounted || !widget.active || _source != 'applications') return;
    if (_apps.failure == null) _askApplications();
  }

  void _askApplications() {
    if (_apps.loaded || _apps.loadingFirst) return;
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted && !_apps.loaded && !_apps.loadingFirst) _apps.refresh();
    });
  }

  void _choose(String source) {
    setState(() => _chosen = source);
    if (source == 'applications') _askApplications();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final picker = SizedBox(
      width: double.infinity,
      child: SegmentedButton<String>(
        key: const Key('databases-source'),
        showSelectedIcon: false,
        segments: [
          ButtonSegment(
            value: 'applications',
            label: Text(l.dbSourceApplications),
          ),
          ButtonSegment(value: 'servers', label: Text(l.dbSourceServers)),
        ],
        selected: {_source},
        onSelectionChanged: (s) => _choose(s.first),
      ),
    );

    return ListenableBuilder(
      listenable: _servers,
      builder: (context, _) => _source == 'applications'
          ? AppDbQueriesList(
              session: widget.session,
              sections: widget.sections,
              sourcePicker: picker,
            )
          : _Servers(
              session: widget.session,
              sections: widget.sections,
              sourcePicker: picker,
            ),
    );
  }
}

String _instanceSortLabel(L l, String sort) => switch (sort) {
  'instance' => l.apmSortInstance,
  'host' => l.apmSortHost,
  'calls' => l.apmSortCalls,
  'avg' => l.dbAvg,
  'aas' => l.apmSortAas,
  'wait' => l.apmSortTopWait,
  'last_seen' => l.apmSortLastSeen,
  _ => l.dbSortTime,
};

class _Servers extends StatelessWidget {
  const _Servers({
    required this.session,
    required this.sections,
    required this.sourcePicker,
  });

  final SessionController session;
  final Sections sections;
  final Widget sourcePicker;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = sections.databases;

    return ListScreen<DbInstance>(
      controller: c,
      baseUrl: session.baseUrl ?? '',
      emptyTitle: l.databasesEmpty,
      search: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          sourcePicker,
          ListSortBar(
            controller: c,
            columns: dbInstanceSorts,
            label: (s) => _instanceSortLabel(l, s),
          ),
        ],
      ),
      footer: LoadMoreTile(controller: c),
      itemBuilder: (context, i) => dbCard(
        context,
        c.items[i],
        onOpen: () => Navigator.of(context).push(
          MaterialPageRoute<void>(
            builder: (_) => DatabaseScreen(
              session: session,
              sections: sections,
              instance: c.items[i].instance,
              dbSystem: c.items[i].dbSystem,
            ),
          ),
        ),
      ),
    );
  }
}
