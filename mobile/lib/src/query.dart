// Running an OQL query by hand.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'session.dart';

/// The query console: one query at a time, and what it answered.
///
/// Not a [ListController]: nothing loads when the screen opens, because a
/// console with no query has nothing to ask. Everything here happens because
/// the person pressed Run.
class QueryController extends ChangeNotifier {
  QueryController(this._client);

  final OpenlogClient _client;

  /// What was last run, which is not the same as what is in the box: the
  /// result on screen belongs to the query that produced it, and the box moves
  /// on as soon as the person types.
  String ran = '';
  OqlResult? result;
  bool running = false;
  SessionFailure? failure;

  /// The last few queries that came back, newest first, so a phone keyboard is
  /// not the only way back to something that worked. Kept in memory only: a
  /// query can name a customer or a host, and this app has no business writing
  /// that to the device.
  final history = <String>[];

  Future<void> run(String query) async {
    final q = query.trim();
    if (q.isEmpty || running) return;
    running = true;
    failure = null;
    notifyListeners();
    try {
      result = await _client.runQuery(q);
      ran = q;
      history
        ..remove(q)
        ..insert(0, q);
      if (history.length > 10) history.removeLast();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      // A query the server rejected is the normal case here, not an outage:
      // the person is writing OQL and the message says what is wrong with it,
      // so it is shown as it came rather than replaced with "something went
      // wrong". 403 is still a permission, not a syntax error.
      failure = switch (e.status) {
        403 => const SessionFailure('queryForbidden', ''),
        400 || 422 => SessionFailure('queryRejected', e.message),
        _ => SessionFailure('unexpected', e.message),
      };
      // The old answer belongs to the old query; keeping it on screen under a
      // new one would be a lie about what the server said.
      result = null;
      ran = '';
    } finally {
      running = false;
      notifyListeners();
    }
  }
}
