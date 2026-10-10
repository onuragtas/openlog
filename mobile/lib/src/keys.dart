// The three kinds of key the settings tabs manage.
//
// They are three endpoints with three shapes, but one story: a list where
// every row is a credential, a form that makes one, and a value the server
// shows exactly once. The one-time value is why these controllers hold a
// `created` -- losing it means issuing another key.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'session.dart';

/// What the three have in common, so the screens can be one widget.
abstract class KeysController<T> extends ChangeNotifier {
  KeysController(this.client);

  final OpenlogClient client;

  List<T> items = const [];
  bool loading = false;

  /// Which row is being revoked, or 'new' while one is being made.
  String? busy;
  SessionFailure? failure;

  /// The value the server showed once, until somebody says they have it.
  String? createdValue;
  String? createdName;

  Future<List<T>> fetch();

  Future<void> load() async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      items = await fetch();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = failureOf(e);
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  void dismissCreated() {
    createdValue = null;
    createdName = null;
    notifyListeners();
  }

  /// Runs a write, then reloads the list without a spinner.
  Future<void> act(String id, Future<void> Function() call) async {
    busy = id;
    failure = null;
    notifyListeners();
    try {
      await call();
      items = await fetch();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = failureOf(e);
    } finally {
      busy = null;
      notifyListeners();
    }
  }

  /// Keeps the value the server handed over once, and reloads the list.
  Future<void> create(String name, Future<String?> Function() call) async {
    busy = 'new';
    failure = null;
    createdValue = null;
    createdName = null;
    notifyListeners();
    try {
      final value = await call();
      // Null is a real answer for an imported license key: the operator
      // chose the value, so there is nothing for the server to reveal.
      createdValue = value;
      createdName = name;
      items = await fetch();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = failureOf(e);
    } finally {
      busy = null;
      notifyListeners();
    }
  }

  SessionFailure failureOf(ApiException e) => switch (e.status) {
    403 => const SessionFailure('keysForbidden', ''),
    _ => SessionFailure('unexpected', e.message),
  };
}

class LicenseKeysController extends KeysController<LicenseKey> {
  LicenseKeysController(super.client);

  @override
  Future<List<LicenseKey>> fetch() async =>
      (await client.licenseKeys()).licenseKeys;

  Future<void> add(String name) => create(name, () async {
    final made = await client.createLicenseKey(name);
    return made.key;
  });

  Future<void> revoke(String id) => act(id, () => client.revokeLicenseKey(id));
}

class ApiKeysController extends KeysController<APIKey> {
  ApiKeysController(super.client);

  @override
  Future<List<APIKey>> fetch() async => (await client.apiKeys()).apiKeys;

  Future<void> add(String name, String role) => create(name, () async {
    final made = await client.createApiKey(name: name, role: role);
    return made.key;
  });

  Future<void> revoke(String id) => act(id, () => client.revokeApiKey(id));
}

class BrowserKeysController extends KeysController<BrowserKey> {
  BrowserKeysController(super.client);

  @override
  Future<List<BrowserKey>> fetch() async =>
      (await client.browserKeys()).browserKeys;

  Future<void> add({
    required String name,
    required String serviceName,
    required String kind,
    required List<String> allowlist,
    String environment = '',
  }) => create(name, () async {
    final made = await client.createBrowserKey(
      name: name,
      serviceName: serviceName,
      kind: kind,
      allowlist: allowlist,
      environment: environment,
    );
    return made.key;
  });

  Future<void> revoke(String id) => act(id, () => client.revokeBrowserKey(id));
}

/// The origins or app ids as typed: one per line, or comma separated.
List<String> parseAllowlist(String text) => [
  for (final part in text.split(RegExp(r'[\n,]+')))
    if (part.trim().isNotEmpty) part.trim(),
];

/// The source maps the server keeps to un-minify browser stacks.
///
/// The same shape as the key tabs minus the form: a phone has no build
/// output to upload, so this lists what is stored and removes one.
class SourceMapsController extends KeysController<SourceMap> {
  SourceMapsController(super.client);

  @override
  Future<List<SourceMap>> fetch() async =>
      (await client.sourceMaps()).sourceMaps;

  Future<void> remove(String id) => act(id, () => client.deleteSourceMap(id));
}
