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
