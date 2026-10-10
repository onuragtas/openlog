// What changed in the organization, newest first.
//
// The server's own record: who did what, with which key, to what. The
// filters are the server's too, because the log is longer than any page and
// a filter applied here would only narrow the page it already sent.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'session.dart';

class AuditController extends ChangeNotifier {
  AuditController(this.client);

  final OpenlogClient client;

  List<AuditEvent> events = const [];

  /// What the actor box and the action box hold, sent to the server.
  String actor = '';
  String action = '';

  /// Where the next page starts, or null when this is the end of the log.
  String? nextCursor;

  bool loading = false;
  bool loadingMore = false;
  SessionFailure? failure;

  Future<void> load() async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      final page = await client.auditLog(actor: actor, action: action);
      events = page.events;
      nextCursor = page.nextCursor;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  /// Reads the next page and keeps what is already on screen.
  ///
  /// The cursor belongs to the filters it was made with, so changing a
  /// filter starts over rather than continuing with a cursor that means
  /// something else.
  Future<void> more() async {
    final cursor = nextCursor;
    if (cursor == null || cursor.isEmpty || loadingMore) return;
    loadingMore = true;
    failure = null;
    notifyListeners();
    try {
      final page = await client.auditLog(
        actor: actor,
        action: action,
        cursor: cursor,
      );
      events = [...events, ...page.events];
      nextCursor = page.nextCursor;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      loadingMore = false;
      notifyListeners();
    }
  }

  SessionFailure _failureOf(ApiException e) => switch (e.status) {
    403 => const SessionFailure('auditForbidden', ''),
    _ => SessionFailure('unexpected', e.message),
  };
}

/// Who made a change, as one line.
///
/// A change made with an API key has no actor e-mail: the key is the actor,
/// and saying "—" there would hide which key could do it.
String auditActor(AuditEvent e, String unknown) {
  final key = e.actorApiKey;
  if (key != null) return key.name.isEmpty ? key.id : key.name;
  return e.actorEmail.isEmpty ? unknown : e.actorEmail;
}
