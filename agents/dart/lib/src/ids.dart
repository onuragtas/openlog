// Trace, span and session identifiers (W3C trace context).
//
// Random only: an id is never derived from anything about the device or the person using it. That is what
// keeps a session a visit rather than a fingerprint (rum.md §1.1), and it is also why Random.secure() is
// used — a predictable session id would let one application's ids be guessed from another's.

import 'dart:math';

final Random _rng = Random.secure();

String _hex(int bytes) {
  final buf = StringBuffer();
  for (var i = 0; i < bytes; i++) {
    buf.write(_rng.nextInt(256).toRadixString(16).padLeft(2, '0'));
  }
  return buf.toString();
}

/// A uniform draw in [0, 1), for the one-per-session sampling decision. It shares the generator above so
/// there is a single source of randomness in the SDK rather than one per question asked of it.
double nextUnit() => _rng.nextDouble();

/// 32 hex characters, the shape the backend requires for `session.id` and a trace id.
String newTraceId() => _hex(16);

/// 16 hex characters, the shape of a span id.
String newSpanId() => _hex(8);

/// A session id has the same shape as a trace id and a different lifetime; named apart so the two are not
/// accidentally interchanged.
String newSessionId() => _hex(16);
