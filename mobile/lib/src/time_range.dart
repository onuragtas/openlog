// The window every screen is about.
//
// A port of `web/src/lib/time.ts`: either a relative range ("15m", "1h",
// "6h", "24h", "7d") or an absolute from/to. The web keeps it in the URL;
// a phone has no URL, so it lives here and the app bar's chip changes it.
//
// Relative is resolved when a request is made, not when the range is
// chosen -- that is what makes "last hour" mean the last hour on every
// refresh instead of the hour in which somebody tapped it.
import 'package:flutter/foundation.dart';

/// The presets the web offers, in its order.
const presetRanges = ['15m', '1h', '6h', '24h', '7d'];

/// What the server itself assumes when no window is sent, so choosing it
/// changes nothing about what was shown before this existed.
const defaultRange = '1h';

const _unitMs = {
  's': 1000,
  'm': 60 * 1000,
  'h': 60 * 60 * 1000,
  'd': 24 * 60 * 60 * 1000,
  'w': 7 * 24 * 60 * 60 * 1000,
};

final _duration = RegExp(r'^(\d+)([smhdw])$');

/// "15m", "1h", "7d" in milliseconds, or null.
int? parseDuration(String? v) {
  if (v == null) return null;
  final m = _duration.firstMatch(v.trim());
  if (m == null) return null;
  final n = int.tryParse(m.group(1)!);
  if (n == null || n <= 0) return null;
  return n * _unitMs[m.group(2)]!;
}

/// A window: either a preset or two instants.
@immutable
class TimeRange {
  const TimeRange.preset(this.range) : from = null, to = null;

  const TimeRange.absolute(DateTime this.from, DateTime this.to) : range = '';

  /// One of [presetRanges], or empty when [from]/[to] are set.
  final String range;
  final DateTime? from;
  final DateTime? to;

  static const initial = TimeRange.preset(defaultRange);

  /// True for a window with two ends: it does not move, so refreshing it
  /// asks the same question again.
  bool get absolute => from != null && to != null;

  /// The window right now. A preset is measured from [now], which is why
  /// this takes it rather than reading the clock: a test must be able to
  /// say when "now" is.
  ({DateTime from, DateTime to}) resolve(DateTime now) {
    final f = from;
    final t = to;
    if (f != null && t != null && f.isBefore(t)) return (from: f, to: t);
    final span = parseDuration(range) ?? parseDuration(defaultRange)!;
    return (from: now.subtract(Duration(milliseconds: span)), to: now);
  }

  /// What a request carries. RFC3339, which the contract takes beside
  /// unix milliseconds and which is the readable one in a log of them.
  Map<String, String> query(DateTime now) {
    final w = resolve(now);
    return {
      'from': w.from.toUtc().toIso8601String(),
      'to': w.to.toUtc().toIso8601String(),
    };
  }

  @override
  bool operator ==(Object other) =>
      other is TimeRange &&
      other.range == range &&
      other.from == from &&
      other.to == to;

  @override
  int get hashCode => Object.hash(range, from, to);
}

/// The chosen window, shared by every screen as the web shares its URL.
class TimeRangeController extends ChangeNotifier {
  TimeRange value = TimeRange.initial;

  /// Returns false when it was already the chosen window, so the caller
  /// does not reload every list for nothing.
  bool choose(TimeRange next) {
    if (next == value) return false;
    value = next;
    notifyListeners();
    return true;
  }
}
