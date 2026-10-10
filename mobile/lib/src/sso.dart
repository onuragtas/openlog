// Single sign-on: the connections, the domains that use them, whether it is
// enforced, which group becomes which role, and the SCIM tokens.
//
// All of it on one settings tab, as on the web. Making a connection is the
// one thing left there: it means pasting a metadata URL or a certificate,
// which is a laptop's job, and the screen says so rather than offering half
// a form.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'session.dart';

class SsoController extends ChangeNotifier {
  SsoController(this.client);

  final OpenlogClient client;

  SSOState? state;
  List<SSODomain> domains = const [];
  List<SSORoleMapping> mappings = const [];
  List<SCIMToken> scimTokens = const [];

  /// The result of the last connection test, by connection id.
  final Map<String, SSOTestResult> tests = {};

  bool loading = false;

  /// Which connection, domain or token is being changed.
  String? busy;
  SessionFailure? failure;

  Future<void> load() async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      state = await client.ssoConnections();
      domains = (await client.ssoDomains()).domains;
      mappings = (await client.ssoRoleMappings()).mappings;
      // Only when the server has SCIM at all: asking otherwise would be a
      // 404 that says nothing about this organization.
      scimTokens = (state?.scimEnabled ?? false)
          ? (await client.scimTokens()).tokens
          : const [];
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  Future<void> setEnabled(String id, {required bool enabled}) =>
      _act(id, () => client.setSsoConnectionEnabled(id, enabled: enabled));

  Future<void> test(String id) async {
    busy = id;
    failure = null;
    notifyListeners();
    try {
      tests[id] = await client.testSsoConnection(id);
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      busy = null;
      notifyListeners();
    }
  }

  Future<void> addDomain(String domain) =>
      _act('new', () => client.addSsoDomain(domain));

  Future<void> verifyDomain(
    String id, {
    required String method,
    String emailLocalPart = '',
  }) => _act(
    id,
    () => client.verifySsoDomain(
      id,
      method: method,
      emailLocalPart: emailLocalPart,
    ),
  );

  Future<void> removeDomain(String id) =>
      _act(id, () => client.deleteSsoDomain(id));

  /// Turns enforcement on or off, keeping the break-glass list as it is.
  ///
  /// The endpoint replaces both: sending the toggle alone would empty the
  /// list and lock everybody into the identity provider.
  Future<void> setEnforce({required bool enforce}) async {
    final current = state?.connection?.breakGlassUserIds ?? const <String>[];
    busy = 'enforce';
    failure = null;
    notifyListeners();
    try {
      state = await client.setSsoEnforcement(
        enforce: enforce,
        breakGlassUserIds: current,
      );
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      busy = null;
      notifyListeners();
    }
  }

  /// Adds or replaces one group mapping and sends the whole set, as the
  /// endpoint takes it.
  Future<void> setMapping(String group, Role role) async {
    final next = [
      for (final m in mappings)
        if (m.group != group) m,
      SSORoleMapping(group: group, role: _mappingRole(role)),
    ];
    await _act('mappings', () => client.putSsoRoleMappings(next));
  }

  Future<void> removeMapping(String group) async {
    final next = [
      for (final m in mappings)
        if (m.group != group) m,
    ];
    await _act('mappings', () => client.putSsoRoleMappings(next));
  }

  Future<void> revokeScimToken(String id) =>
      _act(id, () => client.revokeScimToken(id));

  Future<void> _act(String id, Future<void> Function() call) async {
    busy = id;
    failure = null;
    notifyListeners();
    try {
      await call();
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

  Future<void> _reload() async {
    state = await client.ssoConnections();
    domains = (await client.ssoDomains()).domains;
    mappings = (await client.ssoRoleMappings()).mappings;
  }

  SessionFailure _failureOf(ApiException e) => switch (e.status) {
    403 => const SessionFailure('ssoForbidden', ''),
    _ => SessionFailure('unexpected', e.message),
  };
}

/// Only three roles can come from a group: owner is given by a person, not
/// by an identity provider.
SSORoleMappingRole _mappingRole(Role role) => switch (role) {
  Role.admin => SSORoleMappingRole.admin,
  Role.viewer => SSORoleMappingRole.viewer,
  _ => SSORoleMappingRole.member,
};
