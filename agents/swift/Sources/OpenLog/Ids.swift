// Trace, span and session identifiers (W3C trace context).
//
// Random only: an id is never derived from anything about the device or the person using it. That is what
// keeps a session a visit rather than a fingerprint, and why a cryptographic source is used — a predictable
// session id would let one application's ids be guessed from another's.

import Foundation

private func hex(_ bytes: Int) -> String {
    var out = ""
    out.reserveCapacity(bytes * 2)
    for _ in 0..<bytes {
        out += String(format: "%02x", UInt8.random(in: 0...255))
    }
    return out
}

/// 32 hex characters: the shape the backend requires for `session.id` and a trace id.
func newTraceID() -> String { hex(16) }

/// 16 hex characters: the shape of a span id.
func newSpanID() -> String { hex(8) }

/// Same shape as a trace id, different lifetime; named apart so the two are not interchanged by accident.
func newSessionID() -> String { hex(16) }

/// A uniform draw in [0, 1) for the one-per-session sampling decision.
func nextUnit() -> Double { Double.random(in: 0..<1) }
