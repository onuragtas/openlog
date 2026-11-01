// openlog mobile SDK for Apple platforms (docs/contracts/mobile-agent.md).
//
// Crashes are captured when asked for (Crash.swift). `record(error:)` reports what the application catches.

import Foundation

/// Reads `GET /v1/rum/config`, or nil when it could not be read.
public typealias ConfigFetch = (_ url: String, _ headers: [String: String], _ done: @escaping ([String: Any]?) -> Void) -> Void

public func defaultFetchConfig(_ url: String, _ headers: [String: String], _ done: @escaping ([String: Any]?) -> Void) {
    guard let u = URL(string: url) else { return done(nil) }
    var req = URLRequest(url: u)
    for (k, v) in headers { req.setValue(v, forHTTPHeaderField: k) }
    URLSession.shared.dataTask(with: req) { data, response, _ in
        guard (response as? HTTPURLResponse)?.statusCode == 200, let data,
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        else { return done(nil) }  // the SDK reports with its compiled-in settings; nothing depends on this
        done(obj)
    }.resume()
}

/// The SDK. One per application: a second would double-count every screen.
public final class OpenLog {
    private static let activeLock = NSLock()
    private static var active: OpenLog?

    private let session: SessionState
    private let transport: Transport
    private let visitor: Visitor
    private let lock = NSLock()
    private var userID = ""
    /// The screen last opened, which is where a crash happened.
    private var route = ""
    private var crashes: CrashStore?
    private var crashDiagnostics: AnyObject?
    private let lastSessionStore: UserDefaults?
    /// The session the previous launch ended in, read before this launch overwrites it.
    private var previousSession: LastSession?
    private var rememberedSession = ""
    private let version: String

    private init(session: SessionState, transport: Transport, visitor: Visitor, options: Options) {
        self.session = session
        self.transport = transport
        self.visitor = visitor
        version = options.serviceVersion
        lastSessionStore = options.captureCrashes ? options.visitorDefaults : nil
    }

    /// Starts the SDK. Calling it twice returns the first instance rather than starting a second.
    ///
    /// `send` and `fetchConfig` exist so tests never open a socket; applications leave them unset.
    @discardableResult
    public static func start(
        _ options: Options,
        send: @escaping HTTPSend = defaultSend,
        fetchConfig: @escaping ConfigFetch = defaultFetchConfig
    ) throws -> OpenLog {
        activeLock.lock()
        if let existing = active { activeLock.unlock(); return existing }
        activeLock.unlock()

        let cfg = try resolveConfig(options)  // throws rather than starting a silently useless SDK
        let visitor = Visitor(enabled: options.analyticsCollection, defaults: options.visitorDefaults)
        let session = SessionState(sampleRate: options.sampleRate, roll: {
            let id = visitor.id
            return id.isEmpty ? nextUnit() : visitorRoll(id)
        })
        let sdk = OpenLog(session: session, transport: Transport(cfg: cfg, send: send), visitor: visitor, options: options)
        if options.captureCrashes { sdk.startCrashCapture(options) }

        activeLock.lock()
        active = sdk
        activeLock.unlock()

        // The operator's sample rate wins, so volume can be turned down without shipping a release. Until
        // it answers the application reports with its own, and failure is silent by design.
        // A mobile key refuses a request that names no application, and this one is no exception.
        fetchConfig(cfg.configURL, ["openlog-browser-key": options.key, "openlog-app-id": options.appID]) { conf in
            guard let conf else { return }
            cfg.serviceName = conf["service_name"] as? String ?? ""
            cfg.environment = conf["environment"] as? String ?? ""
            if let rate = conf["sample_rate"] as? Double { sdk.session.sampleRate = rate }
            // Only this answer lets the id reach the device. A server without the field is one without the feature.
            if conf["analytics_mode"] as? String == "visitors" { visitor.confirm() } else { visitor.discard() }
        }
        return sdk
    }

    /// Delivers what an earlier launch left, and starts listening for this one.
    private func startCrashCapture(_ options: Options) {
        let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
        guard let dir = options.crashDirectory ?? support?.appendingPathComponent("openlog-crashes") else { return }
        let store = CrashStore(directory: dir)
        crashes = store
        previousSession = LastSession.read(lastSessionStore)
        rememberSession()
        deliverCrashes(store, send: { [transport] body, done in transport.sendBody(body, done: done) })
        installExceptionHandler(self)
        #if canImport(MetricKit) && (os(iOS) || os(macOS))
        if #available(iOS 14.0, macOS 12.0, *) { crashDiagnostics = CrashDiagnostics(sdk: self) }
        #endif
    }

    /// Kept on every new session, because a process killed by the system gets no chance to say anything.
    private func rememberSession() {
        guard lastSessionStore != nil, session.id != rememberedSession else { return }
        rememberedSession = session.id
        LastSession(id: session.id, version: version).write(lastSessionStore)
    }

    /// An uncaught Objective-C exception. One write and nothing else: the process is going down.
    func record(exception: NSException) {
        guard session.shouldSend else { return }  // a sampled-out session sends nothing at all, crash included
        lock.lock()
        let screen = route
        lock.unlock()
        writeCrash(
            kind: "uncaught_exception", type: exception.name.rawValue, message: exception.reason ?? exception.name.rawValue,
            stack: exception.callStackSymbols.joined(separator: "\n"), sessionID: session.id, sessionUnknown: false,
            screen: screen, version: nil, at: Date()
        )
    }

    /// A crash the operating system recorded about an earlier launch. It carries no session, so it takes the one that
    /// launch ended in, if the build matches. It cannot be sampled against that session, so it follows this one.
    func record(signal: Int32?, reason: String, stack: String, version crashed: String, at: Date) {
        guard session.shouldSend else { return }
        let known = previousSession.flatMap { $0.version == crashed ? $0.id : nil }
        writeCrash(
            kind: "signal", type: signalName(signal), message: reason.isEmpty ? signalName(signal) : reason, stack: stack,
            sessionID: known ?? newSessionID(), sessionUnknown: known == nil, screen: "", version: crashed, at: at
        )
        if let crashes { deliverCrashes(crashes, send: { [transport] body, done in transport.sendBody(body, done: done) }) }
    }

    private func writeCrash(
        kind: String, type: String, message: String, stack: String, sessionID: String, sessionUnknown: Bool,
        screen: String, version: String?, at: Date
    ) {
        guard let crashes else { return }
        let id = newSessionID()
        let span = buildSpan(
            name: "error \(type)",
            event: RumEvent.error,
            traceID: newTraceID(),
            spanID: newSpanID(),
            startMs: Int64(at.timeIntervalSince1970 * 1000),
            attributes: [
                ("session.id", sessionID),
                ("openlog.rum.route", screen),
                ("openlog.rum.error.source", "error"),
                ("openlog.rum.crash.kind", kind),
                ("openlog.rum.crash.id", id),
                ("openlog.rum.crash.session_unknown", sessionUnknown ? "true" : ""),
                ("openlog.rum.visitor.id", visitor.id),
            ],
            isError: true,
            exception: (type: type, message: message, stacktrace: stack)
        )
        if let body = transport.encode([span], version: version) { crashes.write(id: id, body: body) }
    }

    /// Drops the active instance (tests, and shutdown()).
    public static func reset() {
        activeLock.lock()
        active = nil
        activeLock.unlock()
    }

    /// The current session id, or "" when this session was not sampled.
    public var sessionID: String { session.shouldSend ? session.id : "" }

    /// Attaches the application's own identifier for the person to every later span. Pass "" on sign-out.
    ///
    /// **Send an opaque, stable id — not an e-mail address or a name.** openlog stores it and never
    /// interprets it, so the discipline has to live here (mobile-agent.md §3.1).
    public func identify(_ id: String) {
        let trimmed = truncate(id.trimmingCharacters(in: .whitespaces), Limits.maxUserIDBytes)
        lock.lock()
        userID = trimmed
        lock.unlock()
    }

    /// Turns recognising this person across launches on or off, e.g. from a consent dialog (analytics.md §1). Off
    /// deletes the stored id and is remembered across launches. It stops collection and erases nothing already sent:
    /// that is `POST /api/v1/analytics/erase`, called from your backend with `visitorID`.
    public func setAnalyticsCollection(_ enabled: Bool) { visitor.setEnabled(enabled) }

    /// The current visitor id, or "" when there is none.
    public var visitorID: String { visitor.id }

    /// A screen the person opened. `isColdStart` marks the first one of a launch.
    public func record(screen name: String, isColdStart: Bool = false, durationMs: Int64 = 0) {
        let route = truncate(name.trimmingCharacters(in: .whitespaces), Limits.maxNameBytes)
        if route.isEmpty { return }
        lock.lock()
        self.route = route
        lock.unlock()
        if !session.touch() { session.newScreen() }
        emit(RumEvent.screen, "screen \(route)", durationMs: durationMs, spanID: session.screenSpanID, attributes: [
            ("openlog.rum.route", route),
            ("openlog.rum.page_view.kind", isColdStart ? "load" : "route_change"),
        ])
    }

    /// An error the application caught itself.
    public func record(error: Error, stack: String = "") {
        let type = String(describing: Swift.type(of: error))
        emit(RumEvent.error, "error \(type)",
             attributes: [("openlog.rum.error.source", "error")],
             isError: true,
             exception: (type: type, message: "\(error)", stacktrace: stack))
    }

    /// An application-defined event, e.g. `record(event: "checkout_started", params: ["plan": "pro"])`.
    public func record(event name: String, params: [String: Any] = [:]) {
        let n = truncate(name.trimmingCharacters(in: .whitespaces), Limits.maxCustomNameBytes)
        if n.isEmpty { return }  // an unnamed event is an unqueryable row, and the server refuses it anyway
        emit(RumEvent.custom, n, attributes: [("openlog.rum.custom.name", n)] + customParams(params))
    }

    /// An application-defined timing in milliseconds.
    public func record(timing name: String, milliseconds: Int64, params: [String: Any] = [:]) {
        let n = truncate(name.trimmingCharacters(in: .whitespaces), Limits.maxCustomNameBytes)
        if n.isEmpty { return }
        emit(RumEvent.custom, n, durationMs: milliseconds, attributes: [
            ("openlog.rum.custom.name", n),
            ("openlog.rum.custom.value", milliseconds),
            ("openlog.rum.custom.unit", "ms"),
        ] + customParams(params))
    }

    /// Parameters live in a namespace of their own so they can never collide with a field openlog later
    /// learns to interpret. Sorted before the cap is applied: which sixteen survive must depend on the
    /// payload, not on dictionary order.
    private func customParams(_ params: [String: Any]) -> [(String, Any?)] {
        var out: [(String, Any?)] = []
        for key in params.keys.sorted() {
            if out.count >= Limits.maxCustomParams { break }
            let k = key.trimmingCharacters(in: .whitespaces)
            if k.isEmpty || k.count > Limits.maxCustomParamKeyBytes { continue }
            if k.range(of: "^[a-z0-9_.-]+$", options: .regularExpression) == nil { continue }
            out.append(("openlog.rum.custom.param.\(k)", params[key]))
        }
        return out
    }

    private func emit(
        _ event: String,
        _ name: String,
        durationMs: Int64 = 0,
        spanID: String? = nil,
        attributes: [(String, Any?)] = [],
        isError: Bool = false,
        exception: (type: String, message: String, stacktrace: String)? = nil
    ) {
        guard session.shouldSend else { return }  // a sampled-out session sends nothing at all
        session.touch()
        rememberSession()
        lock.lock()
        let user = userID
        lock.unlock()

        var base: [(String, Any?)] = [
            ("session.id", session.id),
            ("openlog.rum.page_view.id", session.screenSpanID),
        ]
        if !user.isEmpty { base.append(("user.id", user)) }
        let visitorID = visitor.id
        if !visitorID.isEmpty { base.append(("openlog.rum.visitor.id", visitorID)) }

        transport.add(buildSpan(
            name: name,
            event: event,
            traceID: session.traceID,
            spanID: spanID ?? newSpanID(),
            startMs: Int64(Date().timeIntervalSince1970 * 1000),
            durationMs: durationMs,
            // Everything hangs off the screen, so one trace holds the whole of it.
            parentSpanID: spanID == nil ? session.screenSpanID : nil,
            attributes: base + attributes,
            isError: isError,
            exception: exception
        ))
    }

    /// Sends what is queued. Call this when the application goes to the background: on a mobile platform
    /// it is the last reliable moment, and the batch most likely to be lost is the final one.
    public func onAppBackgrounded(done: (() -> Void)? = nil) { transport.flush(done: done) }

    public func flush(done: (() -> Void)? = nil) { transport.flush(done: done) }

    public func shutdown(done: (() -> Void)? = nil) {
        if crashes != nil { removeExceptionHandler() }
        #if canImport(MetricKit) && (os(iOS) || os(macOS))
        if #available(iOS 14.0, macOS 12.0, *) { (crashDiagnostics as? CrashDiagnostics)?.stop() }
        #endif
        transport.stop(done: done)
        OpenLog.reset()
    }
}
