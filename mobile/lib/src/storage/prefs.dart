// The settings that are not credentials.
//
// Separate from the token store on purpose: that one is the Keychain,
// because a device session is a bearer credential. How often a screen
// refreshes itself is not, and putting it there would blur a line worth
// keeping sharp.
import 'package:shared_preferences/shared_preferences.dart';

/// What this device remembers between launches. An interface, so a test
/// does not need a platform channel.
abstract class Prefs {
  Future<String?> read(String key);
  Future<void> write(String key, String value);
}

/// The auto-refresh interval, as the web remembers it in localStorage.
const prefAutoRefresh = 'openlog.auto_refresh';

class SharedPrefs implements Prefs {
  @override
  Future<String?> read(String key) async =>
      (await SharedPreferences.getInstance()).getString(key);

  @override
  Future<void> write(String key, String value) async =>
      (await SharedPreferences.getInstance()).setString(key, value);
}

/// For tests, and for a launch with nothing stored yet.
class MemoryPrefs implements Prefs {
  MemoryPrefs([Map<String, String>? values]) : _values = {...?values};

  final Map<String, String> _values;

  @override
  Future<String?> read(String key) async => _values[key];

  @override
  Future<void> write(String key, String value) async => _values[key] = value;
}
