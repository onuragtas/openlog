// The session: a visit, not a person (docs/contracts/mobile-agent.md §4).
//
// 32 random hex characters, expiring after 30 minutes without activity and capped at 4 hours. The two
// bounds answer different questions: the idle timeout ends a visit that stopped, the cap ends one that
// never stops — a screen left open overnight is not a four-hour visit worth one session id.

import Foundation

let sessionIdleTimeout: TimeInterval = 30 * 60
let sessionMaxAge: TimeInterval = 4 * 60 * 60

final class SessionState {
    /// The share of sessions kept, decided **once per session** rather than per event: half a session is
    /// not a cheaper session, it is an unreadable one.
    var sampleRate: Double

    private(set) var id: String = ""
    private(set) var traceID: String = ""
    private(set) var screenSpanID: String = ""
    private(set) var sampled: Bool = false

    private let now: () -> Date
    private var startedAt = Date()
    private var lastSeenAt = Date()

    init(sampleRate: Double, now: @escaping () -> Date = Date.init) {
        self.sampleRate = sampleRate
        self.now = now
        start(at: now())
    }

    private func start(at date: Date) {
        id = newSessionID()
        traceID = newTraceID()
        screenSpanID = newSpanID()
        startedAt = date
        lastSeenAt = date
        sampled = sampleRate >= 1 ? true : (sampleRate <= 0 ? false : nextUnit() < sampleRate)
    }

    /// Records activity, starting a new session when the old one expired. Returns true when a new session
    /// began — which is what a caller needs in order to start a new screen as well.
    @discardableResult
    func touch() -> Bool {
        let at = now()
        if at.timeIntervalSince(lastSeenAt) >= sessionIdleTimeout || at.timeIntervalSince(startedAt) >= sessionMaxAge {
            start(at: at)
            return true
        }
        lastSeenAt = at
        return false
    }

    /// A new screen is a new trace: what it causes belongs to it, not to the screen opened minutes ago.
    func newScreen() {
        traceID = newTraceID()
        screenSpanID = newSpanID()
    }

    /// The server applies the sampling weight from the key, so a payload never carries one.
    var shouldSend: Bool { sampled }
}
