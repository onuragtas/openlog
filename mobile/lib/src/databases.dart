// One database instance: what it is busy with, what it runs, and who is
// connected to it right now.
//
// The web gives an instance three tabs -- Etkinlik, Sorgular, Oturumlar --
// and a screen per statement. The controllers are one per tab rather than
// one per screen, because each answers a different question of the server
// and only the tab being looked at should be asking.
import 'api/client.dart';
import 'api/schema.g.dart';
import 'detail.dart';
import 'list_controller.dart';

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
class DbQueriesController extends ListController<DbQuery> {
  DbQueriesController(this._client, this.instance);

  final OpenlogClient _client;
  final String instance;

  String query = '';

  /// `time`, `calls`, `avg`, `rows`, `errors` or `reads`, as the contract
  /// spells them. Time first, because "where does the time go" is why
  /// anybody opens this list.
  String sort = 'time';

  /// The statement time of the whole instance in the range, which is what
  /// a share is a share of.
  double totalTimeMs = 0;

  @override
  String get forbiddenKind => 'sectionForbidden';

  @override
  Future<List<DbQuery>> fetch() async {
    final page = await _client.dbQueries(
      instance: instance,
      sort: sort,
      q: query.trim(),
    );
    totalTimeMs = page.totalTimeMs;
    return page.queries;
  }
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

/// The sorts the statement list offers, in the web's order.
const dbQuerySorts = ['time', 'calls', 'avg', 'rows', 'errors', 'reads'];

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
