package io.github.onuragtas.openlog

// The session: a visit, not a person (docs/contracts/mobile-agent.md §4).
//
// 32 random hex characters, expiring after 30 minutes without activity and capped at 4 hours. The two
// bounds answer different questions: the idle timeout ends a visit that stopped, the cap ends one that
// never stops — a screen left open overnight is not a four-hour visit worth one session id.

internal const val SESSION_IDLE_TIMEOUT_MS = 30L * 60 * 1000
internal const val SESSION_MAX_AGE_MS = 4L * 60 * 60 * 1000

internal class SessionState(
    var sampleRate: Double,
    private val now: () -> Long = System::currentTimeMillis,
) {
    var id: String = ""
        private set
    var traceId: String = ""
        private set
    var screenSpanId: String = ""
        private set
    var sampled: Boolean = false
        private set

    private var startedAt = 0L
    private var lastSeenAt = 0L

    init { start(now()) }

    private fun start(at: Long) {
        id = newSessionId()
        traceId = newTraceId()
        screenSpanId = newSpanId()
        startedAt = at
        lastSeenAt = at
        sampled = when {
            sampleRate >= 1 -> true
            sampleRate <= 0 -> false
            else -> nextUnit() < sampleRate
        }
    }

    /**
     * Records activity, starting a new session when the old one expired. Returns true when a new session
     * began — which is what a caller needs in order to start a new screen as well.
     */
    fun touch(): Boolean {
        val at = now()
        if (at - lastSeenAt >= SESSION_IDLE_TIMEOUT_MS || at - startedAt >= SESSION_MAX_AGE_MS) {
            start(at)
            return true
        }
        lastSeenAt = at
        return false
    }

    /** A new screen is a new trace: what it causes belongs to it, not to the screen opened minutes ago. */
    fun newScreen() {
        traceId = newTraceId()
        screenSpanId = newSpanId()
    }

    /** The server applies the sampling weight from the key, so a payload never carries one. */
    val shouldSend: Boolean get() = sampled
}
