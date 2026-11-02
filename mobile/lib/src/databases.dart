// One database instance: what it is busy with, what it runs, and who is
// connected to it right now.
//
// The web gives an instance three tabs -- Etkinlik, Sorgular, Oturumlar --
// and a screen per statement. The controllers are one per tab rather than
// one per screen, because each answers a different question of the server
// and only the tab being looked at should be asking.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'detail.dart';
import 'list_controller.dart';
import 'session.dart';

/// A list a server older than this app may not have. Its 404 is an answer, not
/// a failure: the list is empty and the screen says the server does not offer
/// it yet, rather than drawing a red banner over something nothing went wrong
/// with.
mixin OlderServerList<T> on PagedListController<T> {
  bool notOffered = false;

  Future<ListPage<T>> readOrNotOffered(
    Future<ListPage<T>> Function() read,
  ) async {
    try {
      final page = await read();
      notOffered = false;
      return page;
    } on ApiException catch (e) {
      if (!e.noSuchEndpoint) rethrow;
      notOffered = true;
      return ListPage<T>(const []);
    }
  }
}

/// What to call an instance: the server it monitors, because the id itself can
/// be a hash. The id stays what everything is addressed by.
String dbInstanceLabel(DbInstance i) {
  if (i.serverAddress.isEmpty) return i.instance;
  return i.serverPort > 0
      ? '${i.serverAddress}:${i.serverPort}'
      : i.serverAddress;
}

/// The name of a `db.system.name` as people say it.
String dbSystemName(String system) => switch (system) {
  'postgresql' => 'PostgreSQL',
  'mysql' => 'MySQL',
  'mssql' => 'SQL Server',
  _ => system,
};

/// Where the Databases section opens when nobody has chosen: on the servers,
/// unless they answered and there are none. A list that is still loading or
/// that failed says nothing about whether any server reports.
String defaultDatabaseSource({
  required bool loaded,
  required bool failed,
  required int count,
}) => !loaded || failed || count > 0 ? 'servers' : 'applications';

/// The statements of every service, as the applications measured them.
class AppDbQueriesController extends PagedListController<ApmDbQuery>
    with OlderServerList<ApmDbQuery> {
  AppDbQueriesController(this._client)
    : super(sort: 'time', ascendingSorts: const {'statement', 'database'});

  final OpenlogClient _client;

  /// Part of the statement text.
  String query = '';

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<ListPage<ApmDbQuery>> fetchPage(int offset) =>
      readOrNotOffered(() async {
        final page = await _client.apmDbQueries(
          q: query.trim(),
          sort: sort,
          dir: dir,
          limit: limit,
          offset: offset,
        );
        return ListPage(page.queries, hasMore: page.hasMore ?? false);
      });
}

/// The plans agents captured for one statement. Empty is an answer of its own:
/// only an agent with explaining turned on sends any.
class AppDbPlansController extends DetailController<List<ApmDbQueryPlan>> {
  AppDbPlansController(
    this._client, {
    required this.dbSystem,
    required this.statement,
  });

  final OpenlogClient _client;
  final String dbSystem;
  final String statement;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  bool get endpointMayBeMissing => true;

  @override
  Future<List<ApmDbQueryPlan>> fetch() async => (await _client.apmDbQueryPlans(
    dbSystem: dbSystem,
    statement: statement,
  )).plans;
}

/// From an application's statement to the server's view of it. Asked only when
/// somebody taps, and it opens the instance where the statement cost the most,
/// which is the first match the server sends.
class DbLookupController extends ChangeNotifier {
  DbLookupController(this._client);

  final OpenlogClient _client;

  bool loading = false;

  /// The server answered, and no instance holds this statement.
  bool notMonitored = false;

  /// The server has no such endpoint yet.
  bool notOffered = false;
  SessionFailure? failure;

  Future<DbLookupPageMatchesItem?> ask({
    required String dbSystem,
    required String statement,
  }) async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      final page = await _client.dbLookup(
        dbSystem: dbSystem,
        statement: statement,
      );
      notMonitored = page.matches.isEmpty;
      return page.matches.isEmpty ? null : page.matches.first;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      if (e.noSuchEndpoint) {
        notOffered = true;
      } else {
        failure = e.status == 403
            ? const SessionFailure('sectionForbidden', '')
            : SessionFailure('unexpected', e.message);
      }
    } on ApiShapeError catch (e) {
      failure = SessionFailure('unexpected', e.toString());
    } finally {
      loading = false;
      notifyListeners();
    }
    return null;
  }
}

/// Average active sessions per wait type, the waits themselves and the
/// statements the samples were running.
class DbActivityController extends DetailController<DbActivity> {
  DbActivityController(this._client, this.instance);

  final OpenlogClient _client;
  final String instance;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<DbActivity> fetch() => _client.dbActivity(instance);
}

/// The statements of an instance.
class DbQueriesController extends PagedListController<DbQuery> {
  DbQueriesController(this._client, this.instance)
    : super(sort: 'time', ascendingSorts: const {'statement'});

  final OpenlogClient _client;
  final String instance;

  String query = '';

  /// The statement time of the whole instance in the range, which is what
  /// a share is a share of.
  double totalTimeMs = 0;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<ListPage<DbQuery>> fetchPage(int offset) async {
    final page = await _client.dbQueries(
      instance: instance,
      sort: sort,
      dir: dir,
      q: query.trim(),
      limit: limit,
      offset: offset,
    );
    totalTimeMs = page.totalTimeMs;
    return ListPage(page.queries, hasMore: page.hasMore ?? false);
  }
}

/// Every wait event of an instance, as a list of its own: the short one on the
/// activity answer cannot be sorted, and sorting this one reads no chart again.
class DbActivityWaitsController extends PagedListController<DbWait>
    with OlderServerList<DbWait> {
  DbActivityWaitsController(this._client, this.instance)
    : super(sort: 'samples', ascendingSorts: const {'type', 'event'});

  final OpenlogClient _client;
  final String instance;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<ListPage<DbWait>> fetchPage(int offset) => readOrNotOffered(() async {
    final page = await _client.dbActivityWaits(
      instance: instance,
      sort: sort,
      dir: dir,
      limit: limit,
      offset: offset,
    );
    return ListPage(page.waits, hasMore: page.hasMore ?? false);
  });
}

/// Every statement the session samples caught, by load.
class DbActivityQueriesController extends PagedListController<DbActivityQuery>
    with OlderServerList<DbActivityQuery> {
  DbActivityQueriesController(this._client, this.instance)
    : super(sort: 'samples', ascendingSorts: const {'statement', 'wait'});

  final OpenlogClient _client;
  final String instance;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<ListPage<DbActivityQuery>> fetchPage(int offset) =>
      readOrNotOffered(() async {
        final page = await _client.dbActivityQueries(
          instance: instance,
          sort: sort,
          dir: dir,
          limit: limit,
          offset: offset,
        );
        return ListPage(page.queries, hasMore: page.hasMore ?? false);
      });
}

/// The latest session sample of an instance.
class DbSessionsController extends ListController<DbSession> {
  DbSessionsController(this._client, this.instance);

  final OpenlogClient _client;
  final String instance;

  /// When the sample was taken. A session list is a photograph, not a
  /// stream, and a photograph without its time is a lie about "now".
  DateTime? sampledAt;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<List<DbSession>> fetch() async {
    final page = await _client.dbSessions(instance);
    sampledAt = page.sampledAt;
    return page.sessions;
  }
}

/// One statement of one instance.
class DbQueryController extends DetailController<DbQueryDetail> {
  DbQueryController(
    this._client, {
    required this.instance,
    required this.fingerprint,
  });

  final OpenlogClient _client;
  final String instance;
  final String fingerprint;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<DbQueryDetail> fetch() =>
      _client.dbQuery(instance: instance, fingerprint: fingerprint);
}

/// The sorts the statement list offers, in the web's order. Time first,
/// because "where does the time go" is why anybody opens this list.
const dbQuerySorts = [
  'time',
  'statement',
  'calls',
  'avg',
  'rows_per_call',
  'rows',
  'reads',
  'errors',
];

/// The sorts of the instance list, in the web's order. Time has no column
/// there, but it is what the list starts on, so a phone has to be able to come
/// back to it.
const dbInstanceSorts = [
  'time',
  'instance',
  'host',
  'calls',
  'avg',
  'aas',
  'wait',
  'last_seen',
];

const appDbQuerySorts = [
  'time',
  'statement',
  'database',
  'calls',
  'slowest',
  'errors',
];

const dbWaitSorts = ['samples', 'type', 'event'];
const dbActivityQuerySorts = ['samples', 'statement', 'wait'];

/// Sessions as a blocking tree: the ones nobody blocks at the top, each
/// with the sessions waiting on it underneath.
///
/// The web draws the same forest. It is worth the code: "this query is
/// slow" and "this query is waiting for that one" are different problems
/// and a flat list cannot tell them apart.
class SessionNode {
  SessionNode(this.session, this.children);

  final DbSession session;
  final List<SessionNode> children;
}

/// [roots] are the visible heads of every blocking chain, with their
/// waiters under them; [others] is everyone in no chain at all, which on a
/// healthy database is all of them.
///
/// A port of `blockingForest` in `web/src/lib/db.ts`, including its two
/// careful parts: a session whose holder was not sampled is still a head
/// (otherwise the chain would have none and disappear), and a cycle -- two
/// sessions waiting on each other -- starts at whichever member blocks the
/// most, because it has to start somewhere.
({List<SessionNode> roots, List<DbSession> others}) blockingForest(
  List<DbSession> sessions,
) {
  final byId = {for (final s in sessions) s.sessionId: s};
  final waiters = <String, List<DbSession>>{};
  for (final s in sessions) {
    for (final holder in s.blockingSessionIds) {
      (waiters[holder] ??= []).add(s);
    }
  }

  SessionNode build(DbSession s, Set<String> path) {
    final next = {...path, s.sessionId};
    return SessionNode(s, [
      for (final w in waiters[s.sessionId] ?? const <DbSession>[])
        if (!next.contains(w.sessionId)) build(w, next),
    ]);
  }

  final inChain = <String>{
    for (final s in sessions)
      if (s.blockingSessionIds.isNotEmpty || waiters.containsKey(s.sessionId))
        s.sessionId,
  };
  var heads = [
    for (final s in sessions)
      if (inChain.contains(s.sessionId) &&
          !s.blockingSessionIds.any(byId.containsKey))
        s,
  ];
  if (heads.isEmpty) {
    final cyclic = [
      for (final s in sessions)
        if (inChain.contains(s.sessionId)) s,
    ]..sort((a, b) => b.blocks.compareTo(a.blocks));
    heads = cyclic.take(1).toList();
  }
  heads.sort((a, b) {
    final byBlocks = b.blocks.compareTo(a.blocks);
    return byBlocks != 0 ? byBlocks : b.durationMs.compareTo(a.durationMs);
  });

  return (
    roots: [for (final s in heads) build(s, <String>{})],
    others: [
      for (final s in sessions)
        if (!inChain.contains(s.sessionId)) s,
    ],
  );
}
