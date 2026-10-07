// Where the device session token lives between launches.
import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// An address and the token signed in against it.
///
/// The two are kept together because a token means nothing without the address
/// it belongs to: someone who runs their own server and also uses the hosted
/// one must not have a token from one tried against the other.
class StoredSession {
  const StoredSession({required this.baseUrl, required this.token, this.orgId});

  final String baseUrl;
  final String token;

  /// The organization last selected, for a person who belongs to several.
  final String? orgId;

  StoredSession withOrg(String? orgId) =>
      StoredSession(baseUrl: baseUrl, token: token, orgId: orgId);
}

/// Reading and writing that session. An interface because the real one needs
/// Keychain and Keystore, which no unit test can reach.
abstract class TokenStore {
  Future<StoredSession?> read();
  Future<void> write(StoredSession session);
  Future<void> clear();
}

/// Keychain on iOS, EncryptedSharedPreferences on Android.
///
/// A device session lasts 90 days and is a bearer credential: anything that
/// reads it can act as the person until it is revoked, so it does not go in
/// plain preferences next to the chosen theme.
class SecureTokenStore implements TokenStore {
  SecureTokenStore({FlutterSecureStorage? storage})
    : _storage =
          storage ??
          const FlutterSecureStorage(
            aOptions: AndroidOptions(encryptedSharedPreferences: true),
            iOptions: IOSOptions(
              accessibility: KeychainAccessibility.first_unlock,
            ),
          );

  final FlutterSecureStorage _storage;

  static const _baseUrl = 'openlog.base_url';
  static const _token = 'openlog.device_token';
  static const _orgId = 'openlog.org_id';

  @override
  Future<StoredSession?> read() async {
    final baseUrl = await _storage.read(key: _baseUrl);
    final token = await _storage.read(key: _token);
    if (baseUrl == null || token == null) return null;
    return StoredSession(
      baseUrl: baseUrl,
      token: token,
      orgId: await _storage.read(key: _orgId),
    );
  }

  @override
  Future<void> write(StoredSession session) async {
    await _storage.write(key: _baseUrl, value: session.baseUrl);
    await _storage.write(key: _token, value: session.token);
    final orgId = session.orgId;
    if (orgId == null) {
      await _storage.delete(key: _orgId);
    } else {
      await _storage.write(key: _orgId, value: orgId);
    }
  }

  @override
  Future<void> clear() async {
    await _storage.delete(key: _baseUrl);
    await _storage.delete(key: _token);
    await _storage.delete(key: _orgId);
  }
}

/// For tests and for a first launch that has nothing stored yet.
class MemoryTokenStore implements TokenStore {
  MemoryTokenStore([this._session]);

  StoredSession? _session;

  @override
  Future<StoredSession?> read() async => _session;

  @override
  Future<void> write(StoredSession session) async => _session = session;

  @override
  Future<void> clear() async => _session = null;
}
