// Asking again, by itself.
//
// The web's control, with its rules kept: the tick refreshes only what is
// on screen, it is skipped while a request is still in flight, it pauses
// while the app is not in the foreground, and it is off for an absolute
// window -- refetching a window with two fixed ends asks the same
// question again.
//
// The chosen interval is remembered on this device (the web remembers it
// in the browser); the window is not, because an app that silently opens
// on last week is worse than one that always opens on the last hour.
import 'dart:async';

import 'package:flutter/foundation.dart';

import 'storage/prefs.dart';

/// The intervals the web offers, in its order.
const refreshIntervals = ['5s', '10s', '30s', '1m', '5m', '15m'];

/// Which interval is in force, or none.
class AutoRefreshController extends ChangeNotifier {
  AutoRefreshController({Prefs? prefs}) : _prefs = prefs;

  final Prefs? _prefs;

  /// One of [refreshIntervals], or empty for off.
  String interval = '';

  bool get on => interval.isNotEmpty;

  /// The interval in milliseconds, or null when off.
  int? get everyMs => _ms(interval);

  static int? _ms(String v) {
    final m = RegExp(r'^(\d+)([sm])$').firstMatch(v);
    if (m == null) return null;
    final n = int.parse(m.group(1)!);
    return m.group(2) == 's' ? n * 1000 : n * 60 * 1000;
  }

  /// Reads back what this device chose last time.
  Future<void> load() async {
    final stored = await _prefs?.read(prefAutoRefresh);
    if (stored == null || !refreshIntervals.contains(stored)) return;
    interval = stored;
    notifyListeners();
  }

  Future<void> choose(String next) async {
    if (next == interval) return;
    interval = refreshIntervals.contains(next) ? next : '';
    notifyListeners();
    await _prefs?.write(prefAutoRefresh, interval);
  }
}

/// Calls [refresh] every [everyMs], skipping a tick while [busy] says a
/// request is still running and while the app is not in the foreground.
///
/// Separate from the widget that owns it so the rules can be tested
/// without a screen: "skip while busy" and "do not tick in the
/// background" are the two things a timer gets wrong.
class AutoRefreshTimer {
  AutoRefreshTimer({required this.refresh, required this.busy});

  final void Function() refresh;
  final bool Function() busy;

  Timer? _timer;
  bool _foreground = true;

  bool get running => _timer != null;

  void start(int everyMs) {
    stop();
    if (everyMs <= 0) return;
    _timer = Timer.periodic(Duration(milliseconds: everyMs), (_) {
      if (!_foreground || busy()) return;
      refresh();
    });
  }

  /// The app went to the background or came back. A phone spends most of
  /// its life in the first state, and a timer that keeps asking there
  /// spends the battery of somebody who is not looking.
  void setForeground(bool value) => _foreground = value;

  void stop() {
    _timer?.cancel();
    _timer = null;
  }
}
