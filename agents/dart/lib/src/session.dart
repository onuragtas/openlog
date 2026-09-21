// The session: a visit, not a person (docs/contracts/mobile-agent.md §4).
//
// 32 random hex characters, expiring after 30 minutes without activity and capped at 4 hours. Both bounds
// matter and they are different questions: the idle timeout ends a visit that stopped, and the cap ends one
// that never stops — a screen left open overnight is not a four-hour visit worth one session id.

import 'ids.dart';

/// How long a session survives without activity.
const Duration sessionIdleTimeout = Duration(minutes: 30);

/// The longest a single session may run, however busy it is.
const Duration sessionMaxAge = Duration(hours: 4);

/// Tracks the current session and the current screen within it.
class SessionState {
  SessionState(this.sampleRate, {DateTime Function()? now}) : _now = now ?? DateTime.now {
    _start(_now());
  }

  final DateTime Function() _now;

  /// The share of sessions kept, decided **once per session** rather than per event: half a session is not
  /// a cheaper session, it is an unreadable one.
  double sampleRate;

  late String id;
  late String traceId;
  late String screenSpanId;
  late bool sampled;
  late DateTime _startedAt;
  late DateTime _lastSeenAt;

  void _start(DateTime at) {
    id = newSessionId();
    traceId = newTraceId();
    screenSpanId = newSpanId();
    _startedAt = at;
    _lastSeenAt = at;
    sampled = _roll();
  }

  bool _roll() {
    if (sampleRate >= 1) return true;
    if (sampleRate <= 0) return false;
    return nextUnit() < sampleRate;
  }

  /// Records activity, starting a new session when the old one has expired. Returns true when a new session
  /// began, which is what a caller needs to know to start a new screen as well.
  bool touch() {
    final at = _now();
    final expired = at.difference(_lastSeenAt) >= sessionIdleTimeout || at.difference(_startedAt) >= sessionMaxAge;
    if (expired) {
      _start(at);
      return true;
    }
    _lastSeenAt = at;
    return false;
  }

  /// A new screen is a new trace: what it causes belongs to it, not to the screen opened minutes ago.
  void newScreen() {
    traceId = newTraceId();
    screenSpanId = newSpanId();
  }

  /// The server applies the sampling weight from the key, so a payload never carries one (§4).
  bool get shouldSend => sampled;
}
