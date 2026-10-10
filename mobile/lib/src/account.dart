// The person's own account: the password, and the language the server
// writes in.
//
// Separate from SessionController, which is about this device's token: these
// are about the account behind it, and they are what the web's Profil and
// Güvenlik tabs change.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'session.dart';

class AccountController extends ChangeNotifier {
  AccountController(this.client, {this.onMe});

  final OpenlogClient client;

  /// Hands the new `Me` back to the session, so the rest of the app sees the
  /// change without asking again.
  final void Function(Me me)? onMe;

  bool busy = false;
  SessionFailure? failure;

  /// True after a password change, so the screen can say it worked -- and
  /// say the part that is not obvious: the other sessions are gone.
  bool passwordChanged = false;

  Future<bool> changePassword({
    required String currentPassword,
    required String newPassword,
  }) async {
    busy = true;
    failure = null;
    passwordChanged = false;
    notifyListeners();
    try {
      await client.changePassword(
        currentPassword: currentPassword,
        newPassword: newPassword,
      );
      passwordChanged = true;
      return true;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
      return false;
    } on ApiException catch (e) {
      failure = switch (e.status) {
        // The current password was wrong, which is the usual reason and
        // worth its own sentence rather than the server's.
        401 || 403 => const SessionFailure('badCredentials', ''),
        429 => const SessionFailure('rateLimited', ''),
        _ => SessionFailure('unexpected', e.message),
      };
      return false;
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  Future<void> setLanguage(String language) async {
    busy = true;
    failure = null;
    notifyListeners();
    try {
      onMe?.call(await client.setMyLanguage(language));
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = SessionFailure('unexpected', e.message);
    } finally {
      busy = false;
      notifyListeners();
    }
  }
}

/// The organization itself: its name, its ids, and the language the server
/// writes in for everyone in it.
class OrgController extends ChangeNotifier {
  OrgController(this.client);

  final OpenlogClient client;

  Organization? org;
  bool loading = false;
  bool busy = false;
  SessionFailure? failure;

  Future<void> load() async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      org = await client.currentOrg();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _orgFailure(e);
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  /// Renames it, or changes the language. Only what is given is sent: the
  /// endpoint takes both, and passing the other one back would overwrite a
  /// change somebody else made in between.
  Future<bool> update({String? name, String? language}) async {
    busy = true;
    failure = null;
    notifyListeners();
    try {
      org = await client.updateCurrentOrg(name: name, language: language);
      return true;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
      return false;
    } on ApiException catch (e) {
      failure = _orgFailure(e);
      return false;
    } finally {
      busy = false;
      notifyListeners();
    }
  }
}

SessionFailure _orgFailure(ApiException e) => switch (e.status) {
  403 => const SessionFailure('orgForbidden', ''),
  _ => SessionFailure('unexpected', e.message),
};

/// What this account may ask for: a copy of its data, and its own deletion.
///
/// The web calls this the personal data section of the profile tab; the
/// scheduled organization deletions live here too, because the person who
/// can cancel one is the owner looking at their own account.
class PrivacyController extends ChangeNotifier {
  PrivacyController(this.client);

  final OpenlogClient client;

  AccountPrivacy? privacy;
  List<DataExport> exports = const [];

  bool loading = false;
  bool busy = false;
  SessionFailure? failure;

  /// True after an export was queued, so the screen can say it is coming
  /// rather than leave the button looking unpressed.
  bool exportQueued = false;

  /// True after a verification e-mail went out.
  bool verificationSent = false;

  Future<void> load() async {
    loading = true;
    failure = null;
    notifyListeners();
    try {
      final p = await client.accountPrivacy();
      privacy = p;
      exports = p.dataExportEnabled
          ? (await client.personalExports()).exports
          : const [];
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _privacyFailure(e);
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  Future<void> requestExport() async {
    busy = true;
    failure = null;
    exportQueued = false;
    notifyListeners();
    try {
      await client.requestPersonalExport();
      exportQueued = true;
      exports = (await client.personalExports()).exports;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _privacyFailure(e);
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  Future<void> resendVerification() async {
    busy = true;
    failure = null;
    verificationSent = false;
    notifyListeners();
    try {
      await client.resendVerificationEmail();
      verificationSent = true;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = switch (e.status) {
        // Already verified, which is not a failure worth a red banner.
        409 => const SessionFailure('alreadyVerified', ''),
        429 => const SessionFailure('rateLimited', ''),
        _ => SessionFailure('unexpected', e.message),
      };
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  /// Schedules the organization's deletion. It runs after the grace period
  /// the server states, and can be cancelled until then.
  Future<bool> deleteOrganization({
    required String confirmName,
    String password = '',
  }) async {
    busy = true;
    failure = null;
    notifyListeners();
    try {
      await client.scheduleOrgDeletion(
        confirmName: confirmName,
        password: password,
      );
      await _reload();
      return true;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
      return false;
    } on ApiException catch (e) {
      failure = _privacyFailure(e);
      return false;
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  Future<void> cancelOrgDeletion(String id) async {
    busy = true;
    failure = null;
    notifyListeners();
    try {
      await client.cancelOrgDeletion(id);
      await _reload();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _privacyFailure(e);
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  /// Deletes this account, then signs the device out: the token it holds
  /// belongs to an account that no longer exists.
  Future<bool> deleteAccount({
    required String confirmEmail,
    String password = '',
    required Future<void> Function() signOut,
  }) async {
    busy = true;
    failure = null;
    notifyListeners();
    try {
      await client.deleteAccount(
        confirmEmail: confirmEmail,
        password: password,
      );
      await signOut();
      return true;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
      return false;
    } on ApiException catch (e) {
      failure = _privacyFailure(e);
      return false;
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  Future<void> _reload() async {
    privacy = await client.accountPrivacy();
  }
}

/// A timestamp the contract types as a plain string.
///
/// Parsed for display and handed back as it came when it will not parse:
/// an unreadable date is better than a wrong one.
String whenOf(String raw, String Function(DateTime) relative) {
  final t = DateTime.tryParse(raw);
  return t == null ? raw : relative(t.toUtc());
}

SessionFailure _privacyFailure(ApiException e) => switch (e.status) {
  // The password was wrong, or the SSO session is too old to count as
  // re-authentication -- both mean "prove it is you" rather than "no".
  401 || 403 => const SessionFailure('reauthNeeded', ''),
  429 => const SessionFailure('rateLimited', ''),
  _ => SessionFailure('unexpected', e.message),
};
