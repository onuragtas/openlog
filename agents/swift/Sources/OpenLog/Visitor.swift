// The persistent visitor id behind DAU, MAU and retention (docs/contracts/analytics.md §1).
//
// Unlike a session this recognises someone across launches, so what it may do is narrower than what it looks like:
//
//  - It reaches UserDefaults only once the server said this key is in `visitors` mode. Until then it lives in memory.
//  - A withdrawal outlives the process. Otherwise the next launch would hand someone who just said no a new id.
//  - Without a store there is no id at all. A new one per launch would report every launch as a new person, which is
//    worse than reporting nothing: DAU and new users would look real and be wrong.

import Foundation

private let storageKey = "openlog.rum.visitor"
private let optOutValue = "optout"

/// Rotated after 13 months, matching how long the server keeps an actor (analytics.md §1.2).
let visitorMaxAge: TimeInterval = 395 * 24 * 60 * 60

/// Where this visitor falls in [0, 1): FNV-1a of the id over 2^32. Sampling is once per visitor when there is one
/// (analytics.md §2). Every SDK computes exactly this, so one person is in or out of the sample on all of them.
func visitorRoll(_ id: String) -> Double {
    var h: UInt32 = 2166136261
    for byte in id.utf8 {
        h = (h ^ UInt32(byte)) &* 16777619
    }
    return Double(h) / 4294967296
}

final class Visitor {
    private let defaults: UserDefaults?
    private let now: () -> Date
    private let lock = NSLock()
    private var current = ""
    private var createdAt: TimeInterval = 0
    private var persistent = false

    init(enabled: Bool, defaults: UserDefaults?, now: @escaping () -> Date = Date.init) {
        self.defaults = defaults
        self.now = now
        // An id in storage survives a default of false: an application that asks first starts every launch disabled
        // and enables after reading its own consent, and the person it asked must not become new each time.
        if enabled && !read().optOut { adopt() }
    }

    /// The id to send, or "" when there is none.
    var id: String {
        lock.lock()
        defer { lock.unlock() }
        return current
    }

    private func read() -> (id: String, createdAt: TimeInterval, optOut: Bool) {
        guard let raw = defaults?.string(forKey: storageKey) else { return ("", 0, false) }
        if raw == optOutValue { return ("", 0, true) }
        let parts = raw.split(separator: "\n").map(String.init)
        guard parts.count >= 2, let at = TimeInterval(parts[1]) else { return ("", 0, false) }
        return (parts[0], at, false)
    }

    private func adopt() {
        guard defaults != nil else { return }
        let stored = read()
        let at = now().timeIntervalSince1970
        let wellFormed = stored.id.range(of: "^[0-9a-f]{32}$", options: .regularExpression) != nil
        let fresh = wellFormed && at - stored.createdAt < visitorMaxAge
        current = fresh ? stored.id : newSessionID()
        createdAt = fresh ? stored.createdAt : at
    }

    private func persist() {
        defaults?.set("\(current)\n\(createdAt)", forKey: storageKey)
    }

    /// The server says this key recognises visitors.
    func confirm() {
        lock.lock()
        defer { lock.unlock() }
        persistent = true
        if !current.isEmpty { persist() }
    }

    /// The server says this key does not: nothing stays on the device, except a person's own withdrawal.
    func discard() {
        lock.lock()
        defer { lock.unlock() }
        persistent = false
        current = ""
        if !read().optOut { defaults?.removeObject(forKey: storageKey) }
    }

    /// Disabling deletes the id, so enabling again starts a new one.
    func setEnabled(_ enabled: Bool) {
        lock.lock()
        defer { lock.unlock() }
        if !enabled {
            current = ""
            defaults?.set(optOutValue, forKey: storageKey)
            return
        }
        if !current.isEmpty { return }
        let wasOptOut = read().optOut
        adopt()
        if persistent && !current.isEmpty {
            persist()
        } else if wasOptOut {
            defaults?.removeObject(forKey: storageKey)
        }
    }
}
