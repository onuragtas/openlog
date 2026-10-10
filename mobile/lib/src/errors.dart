// The error inbox of every service, and one group's workflow.
//
// An inbox that can only be read is a list of things to do somewhere else, so
// the writes are here too: resolve, resolve in a version, ignore, reopen,
// take it, and say something about it. The one thing left on the web is
// assigning it to somebody else, which needs the member list.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'list_controller.dart';
import 'session.dart';

/// The status tabs, in the web's order. `all` is this app's word for "no
/// status filter": the server takes the parameter away rather than taking a
/// fourth value.
const errorStatuses = <String>['unresolved', 'resolved', 'ignored', 'all'];

/// What the list is sorted by, which the server does.
const errorSorts = <String>['count', 'last_seen', 'first_seen'];

/// A resolved group that reopened by itself and has not been resolved again.
///
/// Worth its own word on the row: "unresolved" says somebody has to look,
/// "regressed" says somebody already fixed this and it came back.
bool isRegressed(ApmErrorGroup g) =>
    g.status == ApmErrorStatus.unresolved && g.regressedAt != null;

/// The inbox.
class ErrorInboxController extends ListController<ApmErrorGroup> {
  ErrorInboxController(this.client);

  final OpenlogClient client;

  /// The search box, sent to the server rather than filtered here.
  String query = '';
  String status = 'unresolved';
  String sort = 'count';

  /// How many groups each status has, after the search and before the status
  /// filter -- which is what makes the counts worth showing next to the tabs.
  ApmErrorInboxCounts? counts;

  /// False without PostgreSQL: there is no workflow at all and every group
  /// reads as unresolved. The screen says so instead of offering buttons
  /// that cannot work.
  bool workflow = true;

  /// More groups matched than came back.
  bool truncated = false;

  @override
  String get forbiddenKind => 'servicesForbidden';

  @override
  Future<List<ApmErrorGroup>> fetch() async {
    final inbox = await client.apmErrors(status: status, q: query, sort: sort);
    counts = inbox.counts;
    workflow = inbox.workflow;
    truncated = inbox.truncated;
    return inbox.groups;
  }

  /// Changes one group's status and reloads.
  ///
  /// The reload is what moves the row out of the tab it no longer belongs
  /// in; keeping it would show a resolved group under "unresolved" until
  /// somebody pulled to refresh.
  Future<void> setStatus(
    String groupId,
    String status, {
    String? version,
  }) async {
    busy = groupId;
    failure = null;
    notifyListeners();
    try {
      await client.patchApmErrorGroups(
        [groupId],
        status: status,
        resolvedInVersion: version,
      );
      await refresh();
    } on ApiUnreachable {
      await refresh();
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      final reported = _failureOf(e);
      await refresh();
      failure = reported;
    } finally {
      busy = null;
      notifyListeners();
    }
  }

  /// Which group is being changed, so only that row is busy.
  String? busy;
}

SessionFailure _failureOf(ApiException e) => switch (e.status) {
  403 => const SessionFailure('servicesForbidden', ''),
  _ => SessionFailure('unexpected', e.message),
};

/// One group: its comments, and the actions on it.
///
/// The group itself comes from the list that opened the screen -- it is
/// already loaded, and re-fetching one group is not an endpoint.
class ErrorGroupController extends ChangeNotifier {
  ErrorGroupController(this.client, this.group);

  final OpenlogClient client;

  /// Replaced after a change, from the reloaded list, so the screen shows
  /// what the server now has rather than what was tapped.
  ApmErrorGroup group;

  /// Who is signed in, to decide which comments they may delete. Empty when
  /// unknown, which hides the delete rather than showing one that 403s.
  String myUserId = '';

  /// Everything the web's panel shows, once it has been read. The list's
  /// row is enough to draw the top of the screen; this is the rest.
  ApmErrorGroupDetail? detail;

  List<ApmErrorComment> comments = const [];
  bool loading = false;
  bool busy = false;
  SessionFailure? failure;

  Future<void> load() async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      // One request rather than two: the detail carries the comments with
      // it, and asking for them separately would show a different list than
      // the one the rest of the screen was drawn from.
      final d = await client.apmErrorGroup(
        service: group.serviceName,
        groupId: group.groupId,
      );
      detail = d;
      comments = d.comments;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  Future<void> comment(String body) async {
    busy = true;
    failure = null;
    notifyListeners();
    try {
      await client.addApmErrorComment(group.groupId, body);
      comments = (await client.apmErrorComments(group.groupId)).comments;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  Future<void> deleteComment(String id) async {
    busy = true;
    failure = null;
    notifyListeners();
    try {
      await client.deleteApmErrorComment(group.groupId, id);
      comments = (await client.apmErrorComments(group.groupId)).comments;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  /// Whether this person may delete [c]: its author, or an admin or owner.
  ///
  /// The role is not known here, so this answers only for the author; an
  /// admin deleting somebody else's comment does it on the web. A button
  /// that 403s would be worse than no button.
  bool canDelete(ApmErrorComment c) =>
      myUserId.isNotEmpty && c.authorUserId == myUserId;
}

/// The longest a comment may be, in bytes, as the contract says.
const commentMaxBytes = 4000;
