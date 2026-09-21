// Batching and delivery (docs/contracts/mobile-agent.md §5, §6).
//
// Sending is injectable so the tests never open a socket: what is worth testing here is when a batch goes
// out and what happens to it when the server says no, neither of which needs a network to be wrong.

import Foundation

/// Posts a body and answers with the HTTP status, or -1 when the request never completed.
public typealias HTTPSend = (_ url: String, _ headers: [String: String], _ body: Data, _ done: @escaping (Int) -> Void) -> Void

/// The default sender. URLSession rather than a package: this SDK has no dependencies, and an application
/// must never have to resolve a version of one because its telemetry library wanted it.
public func defaultSend(_ url: String, _ headers: [String: String], _ body: Data, _ done: @escaping (Int) -> Void) {
    guard let u = URL(string: url) else { return done(-1) }
    var req = URLRequest(url: u)
    req.httpMethod = "POST"
    req.httpBody = body
    for (k, v) in headers { req.setValue(v, forHTTPHeaderField: k) }
    URLSession.shared.dataTask(with: req) { _, response, _ in
        done((response as? HTTPURLResponse)?.statusCode ?? -1)
    }.resume()
}

final class Transport {
    private let cfg: ResolvedConfig
    private let send: HTTPSend
    private let lock = NSLock()
    private var queue: [[String: Any]] = []
    private var timer: DispatchSourceTimer?

    /// Set when the server refused the key itself. Nothing about the next attempt would differ, so the SDK
    /// stops rather than spending the device's battery on a permanent answer (§6).
    private(set) var refused = false

    init(cfg: ResolvedConfig, send: @escaping HTTPSend = defaultSend) {
        self.cfg = cfg
        self.send = send
    }

    func add(_ span: [String: Any]) {
        lock.lock()
        if refused { lock.unlock(); return }
        if queue.count >= Limits.maxSpansPerRequest {
            // Drop the oldest: the newest events are the ones still worth having, and an unbounded queue
            // in a long-lived application is a leak the application would be blamed for.
            queue.removeFirst()
        }
        queue.append(span)
        let full = queue.count >= cfg.options.maxBatchSize
        lock.unlock()
        if full { flush() } else { schedule() }
    }

    private func schedule() {
        lock.lock()
        defer { lock.unlock() }
        guard timer == nil else { return }
        let t = DispatchSource.makeTimerSource(queue: .global(qos: .utility))
        t.schedule(deadline: .now() + cfg.options.flushInterval)
        t.setEventHandler { [weak self] in
            self?.clearTimer()
            self?.flush()
        }
        timer = t
        t.resume()
    }

    private func clearTimer() {
        lock.lock()
        timer?.cancel()
        timer = nil
        lock.unlock()
    }

    /// Sends what is queued. `done` is for tests and for stop(); applications do not need it.
    func flush(done: (() -> Void)? = nil) {
        clearTimer()
        lock.lock()
        guard !refused, !queue.isEmpty else { lock.unlock(); done?(); return }
        let batch = queue
        queue.removeAll()
        lock.unlock()

        guard let body = try? JSONSerialization.data(withJSONObject: buildPayload(spans: batch, resource: resource())) else {
            done?()  // an unencodable payload is dropped rather than retried: it will not encode next time
            return
        }
        send(cfg.rumURL, [
            "content-type": "application/json",
            "openlog-browser-key": cfg.options.key,
            "openlog-app-id": cfg.options.appID,
        ], body) { [weak self] status in
            self?.finish(status: status, batch: batch)
            done?()
        }
    }

    private func finish(status: Int, batch: [[String: Any]]) {
        if status == 401 || status == 403 {
            lock.lock()
            refused = true
            lock.unlock()
            log("the server refused this key (\(status)); nothing further will be sent")
            return
        }
        // 429 and 503 are temporary and say so; the batch goes back so the next flush carries it. Anything
        // else — including a request that never completed — is dropped rather than retried forever.
        guard status == 429 || status == 503 || status == -1 else { return }
        lock.lock()
        let room = max(0, Limits.maxSpansPerRequest - queue.count)
        queue.insert(contentsOf: batch.prefix(room), at: 0)
        lock.unlock()
        schedule()
    }

    private func resource() -> [(String, Any?)] {
        [
            ("service.version", cfg.options.serviceVersion),
            ("device.model.identifier", cfg.options.deviceModel),
            ("device.manufacturer", cfg.options.deviceManufacturer),
            ("os.name", cfg.options.osName),
            ("os.version", cfg.options.osVersion),
        ]
    }

    private func log(_ message: String) {
        if cfg.options.debug { FileHandle.standardError.write(Data("[openlog] \(message)\n".utf8)) }
    }

    /// Sends what is queued and stops the timer. Called when the application goes to the background, which
    /// is the last reliable moment on a mobile platform.
    func stop(done: (() -> Void)? = nil) {
        flush(done: done)
        clearTimer()
    }
}
