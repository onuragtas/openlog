// The one piece of mutable state above the widgets: which installation, which
// person, which organization.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'storage/token_store.dart';

/// Where the app is in getting to a usable screen.
enum SessionStage {
  /// Reading what was stored, before the first frame decides anything.
  restoring,

  /// No address yet, or the person asked to change it.
  needsServer,

  /// The address answered and is an openlog server; nobody is signed in.
  needsSignIn,

  /// Signed in, with [SessionController.me] filled in.
  signedIn,
}

/// Why the last action did not work, in a form a screen can show.
class SessionFailure {
  const SessionFailure(this.kind, this.detail);

  /// `unreachable`, `notOpenlog`, `badAddress`, `badCredentials`,
  /// `rateLimited`, `signUpClosed`, `signUpNeedsCaptcha` or `unexpected`.
  final String kind;

  /// The server's own message when there was one, for `unexpected`.
  final String detail;
}

/// Holds the address, the token and the person, and is the only thing that
/// writes to storage.
///
/// A [ChangeNotifier] rather than a state-management package: there is one
/// object and three screens listen to it, and a dependency whose job is to pass
/// that object around would be more machinery than the problem.
class SessionController extends ChangeNotifier {
  SessionController({
    required TokenStore store,
    OpenlogClient Function(String baseUrl)? openClient,
  }) : _store = store,
       _open = openClient ?? ((baseUrl) => OpenlogClient(baseUrl: baseUrl));

  final TokenStore _store;
  final OpenlogClient Function(String baseUrl) _open;

  SessionStage stage = SessionStage.restoring;
  SessionFailure? failure;
  bool busy = false;

  OpenlogClient? _client;

  /// The address being used, normalized. Null before one is chosen.
  String? get baseUrl => _client?.baseUrl;

  /// What that server allows: whether sign-up is offered, how long a password
  /// must be, whether a CAPTCHA stands in the way, whether SSO exists.
  AuthConfig? authConfig;

  /// The signed-in person, their organizations and their role in the selected
  /// one. Null until [stage] is [SessionStage.signedIn].
  Me? me;

  /// The selected organization, for a person who belongs to several.
  OrgRef? get organization => me?.organization;

  @override
  void dispose() {
    _client?.close();
    super.dispose();
  }

  void _settle({
    SessionStage? stage,
    SessionFailure? failure,
    bool busy = false,
  }) {
    if (stage != null) this.stage = stage;
    this.failure = failure;
    this.busy = busy;
    notifyListeners();
  }

  /// Reads what was stored and, if there is a token, checks it still works.
  ///
  /// The check matters: a session can have been revoked from the web, the
  /// person removed from the organization or their role changed, and all three
  /// should land on the sign-in screen rather than on a screen that 401s.
  Future<void> restore() async {
    final stored = await _store.read();
    if (stored == null) {
      _settle(stage: SessionStage.needsServer);
      return;
    }
    try {
      final client = _open(stored.baseUrl);
      _client?.close();
      _client = client;
      authConfig = await client.authConfig();
      client.token = stored.token;
      client.orgId = stored.orgId;
      me = await client.me();
      _settle(stage: SessionStage.signedIn);
    } on ApiException catch (e) {
      // 401 and 403 mean the token is gone or no longer allowed; keeping it
      // would make every later screen fail the same way.
      if (e.isUnauthenticated || e.isForbidden) {
        await _store.clear();
        _client?.token = null;
        _settle(stage: SessionStage.needsSignIn);
        return;
      }
      _settle(stage: SessionStage.needsSignIn, failure: _failureOf(e));
    } on ApiUnreachable {
      // The token is probably fine and the network is not. Do not sign the
      // person out for being on a train.
      _settle(
        stage: SessionStage.needsSignIn,
        failure: const SessionFailure('unreachable', ''),
      );
    }
  }

  /// Points the app at an address and reads what it allows.
  Future<void> useServer(String input) async {
    _settle(busy: true);
    try {
      final client = _open(input);
      authConfig = await client.authConfig();
      _client?.close();
      _client = client;
      _settle(stage: SessionStage.needsSignIn);
    } on FormatException {
      _settle(
        stage: SessionStage.needsServer,
        failure: const SessionFailure('badAddress', ''),
      );
    } on ApiUnreachable {
      _settle(
        stage: SessionStage.needsServer,
        failure: const SessionFailure('unreachable', ''),
      );
    } on ApiException catch (e) {
      // A 404 or a body that is not JSON means something answered but it was
      // not openlog -- a different product, a parked domain, a captive portal.
      //
      // The stage is named rather than left alone: an address that did not work
      // has to leave the person on the screen that can correct it, whatever
      // screen they came from.
      _settle(
        stage: SessionStage.needsServer,
        failure: e.code.isEmpty
            ? const SessionFailure('notOpenlog', '')
            : _failureOf(e),
      );
    }
  }

  /// Forgets the address so the first screen is shown again. The token goes
  /// too: it belongs to the address being left.
  Future<void> forgetServer() async {
    await _store.clear();
    _client?.close();
    _client = null;
    authConfig = null;
    me = null;
    _settle(stage: SessionStage.needsServer);
  }

  Future<void> signIn({
    required String email,
    required String password,
    required String deviceName,
  }) async {
    final client = _client;
    if (client == null) {
      _settle(stage: SessionStage.needsServer);
      return;
    }
    _settle(busy: true);
    try {
      final session = await client.signIn(
        email: email,
        password: password,
        deviceName: deviceName,
      );
      me = session.me;
      await _store.write(
        StoredSession(
          baseUrl: client.baseUrl,
          token: session.token,
          orgId: session.me.organization?.id,
        ),
      );
      _settle(stage: SessionStage.signedIn);
    } on ApiUnreachable {
      _settle(failure: const SessionFailure('unreachable', ''));
    } on ApiException catch (e) {
      _settle(failure: _failureOf(e));
    }
  }

  /// Creates an account, its organization, and signs this device in.
  ///
  /// Refused before it is attempted when the server has closed sign-up or asks
  /// for a CAPTCHA, because both are answers the screen can give without a
  /// round trip -- and the CAPTCHA one has to send the person to a browser.
  Future<void> signUp({
    required String email,
    required String password,
    required String name,
    required String organizationName,
    required String deviceName,
  }) async {
    final client = _client;
    final config = authConfig;
    if (client == null || config == null) {
      _settle(stage: SessionStage.needsServer);
      return;
    }
    if (!config.signupEnabled) {
      _settle(failure: const SessionFailure('signUpClosed', ''));
      return;
    }
    if (config.captcha != null) {
      _settle(failure: const SessionFailure('signUpNeedsCaptcha', ''));
      return;
    }
    _settle(busy: true);
    try {
      final session = await client.signUp(
        email: email,
        password: password,
        name: name,
        organizationName: organizationName,
        deviceName: deviceName,
      );
      me = session.me;
      await _store.write(
        StoredSession(
          baseUrl: client.baseUrl,
          token: session.token,
          orgId: session.me.organization?.id,
        ),
      );
      _settle(stage: SessionStage.signedIn);
    } on ApiUnreachable {
      _settle(failure: const SessionFailure('unreachable', ''));
    } on ApiException catch (e) {
      _settle(
        failure: e.status == 409
            ? const SessionFailure('emailTaken', '')
            : _failureOf(e),
      );
    }
  }

  /// Acts in another of the person's organizations from now on.
  Future<void> switchOrganization(String orgId) async {
    final client = _client;
    if (client == null) return;
    final previous = client.orgId;
    _settle(busy: true);
    client.orgId = orgId;
    try {
      me = await client.me();
      final token = client.token;
      if (token != null) {
        await _store.write(
          StoredSession(baseUrl: client.baseUrl, token: token, orgId: orgId),
        );
      }
      _settle(stage: SessionStage.signedIn);
    } on ApiException catch (e) {
      // Put it back rather than leaving the app acting in an organization the
      // screen does not say it is in.
      client.orgId = previous;
      _settle(failure: _failureOf(e));
    } on ApiUnreachable {
      client.orgId = previous;
      _settle(failure: const SessionFailure('unreachable', ''));
    }
  }

  /// Ends the session on the server and forgets it here.
  ///
  /// The local state is cleared whatever the server said: the person asked to
  /// be signed out, and staying signed in because the network was down is the
  /// opposite of that. The session expires on its own, and can be signed out
  /// from the web in the meantime.
  Future<void> signOut() async {
    _settle(busy: true);
    try {
      await _client?.signOut();
    } on ApiException {
      // Reported by the server; nothing to do about it here.
    } on ApiUnreachable {
      // Offline; the server-side session outlives this, by design.
    } finally {
      await _store.clear();
      me = null;
      _settle(stage: SessionStage.needsSignIn);
    }
  }

  SessionFailure _failureOf(ApiException e) {
    if (e.status == 401) return const SessionFailure('badCredentials', '');
    if (e.status == 429) return const SessionFailure('rateLimited', '');
    return SessionFailure('unexpected', e.message);
  }
}
