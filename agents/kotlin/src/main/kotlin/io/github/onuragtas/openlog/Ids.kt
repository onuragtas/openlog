package io.github.onuragtas.openlog

import java.security.SecureRandom

// Trace, span and session identifiers (W3C trace context).
//
// Random only: an id is never derived from anything about the device or the person using it. That is what
// keeps a session a visit rather than a fingerprint, and why the source is cryptographic — a predictable
// session id would let one application's ids be guessed from another's.

private val rng = SecureRandom()

private fun hex(bytes: Int): String {
    val buf = ByteArray(bytes)
    rng.nextBytes(buf)
    val out = StringBuilder(bytes * 2)
    for (b in buf) out.append(String.format("%02x", b))
    return out.toString()
}

/** 32 hex characters: the shape the backend requires for `session.id` and a trace id. */
internal fun newTraceId(): String = hex(16)

/** 16 hex characters: the shape of a span id. */
internal fun newSpanId(): String = hex(8)

/** Same shape as a trace id, different lifetime; named apart so the two are not interchanged by accident. */
internal fun newSessionId(): String = hex(16)

/** A uniform draw in [0, 1) for the one-per-session sampling decision. */
internal fun nextUnit(): Double = rng.nextDouble()
