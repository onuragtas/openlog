// Who is in the organization, and who has been asked to join.
//
// The web keeps both on one settings tab, because they are the same
// question at two stages: a person is invited, then they are a member.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'session.dart';

class MembersController extends ChangeNotifier {
  MembersController(this.client);

  final OpenlogClient client;

  List<Member> members = const [];
  List<Invitation> invitations = const [];

  /// The last invitation's one-time link. Shown until it is dismissed: the
  /// server sends it by e-mail when it can, and hands it over exactly once
  /// either way, so losing it here means issuing a new one.
  InvitationCreated? created;

  bool loading = false;

  /// Which member or invitation is being changed, so only that row is busy.
  String? busy;
  SessionFailure? failure;

  Future<void> load() async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      members = (await client.members()).members;
      invitations = (await client.invitations()).invitations;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  Future<void> setRole(String userId, Role role) =>
      _act(userId, () => client.setMemberRole(userId, role.wire));

  Future<void> remove(String userId) =>
      _act(userId, () => client.removeMember(userId));

  Future<void> revoke(String id) => _act(id, () => client.revokeInvitation(id));

  Future<void> invite({required String email, required Role role}) async {
    busy = 'new';
    failure = null;
    created = null;
    notifyListeners();
    try {
      created = await client.createInvitation(email: email, role: role.wire);
      await _reload();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      busy = null;
      notifyListeners();
    }
  }

  Future<void> resend(String id) async {
    busy = id;
    failure = null;
    created = null;
    notifyListeners();
    try {
      // The previous link stops working, so the new token is the only one
      // there is: it goes on screen for the same reason a new one does.
      created = await client.resendInvitation(id);
      await _reload();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      busy = null;
      notifyListeners();
    }
  }

  /// Hides the one-time link once somebody has passed it on.
  void dismissCreated() {
    created = null;
    notifyListeners();
  }

  Future<void> _act(String id, Future<void> Function() call) async {
    busy = id;
    failure = null;
    notifyListeners();
    try {
      await call();
      await _reload();
    } on ApiUnreachable {
      await _reload();
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      final reported = _failureOf(e);
      await _reload();
      failure = reported;
    } finally {
      busy = null;
      notifyListeners();
    }
  }

  /// Reads both lists again without touching `loading`, so the screen does
  /// not blink back to a spinner after every change.
  Future<void> _reload() async {
    members = (await client.members()).members;
    invitations = (await client.invitations()).invitations;
  }

  SessionFailure _failureOf(ApiException e) => switch (e.status) {
    403 => const SessionFailure('membersForbidden', ''),
    // 409 is the server refusing to leave the organization without an
    // owner. Its own sentence says which, so it is passed through.
    _ => SessionFailure('unexpected', e.message),
  };
}
