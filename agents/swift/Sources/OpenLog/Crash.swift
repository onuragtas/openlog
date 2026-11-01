// Crash capture (docs/contracts/mobile-crash.md §2).
//
// Two sources, because a Swift trap and an Objective-C exception die differently. MetricKit has the operating system
// report signals and Mach exceptions on a later launch, so nothing here has to run inside a crashing process: a signal
// handler may only call async-signal-safe functions, and getting that wrong hides the crash. An uncaught NSException does
// run a handler, which writes one file and returns.

import Foundation
#if canImport(MetricKit) && (os(iOS) || os(macOS))
import MetricKit
#endif

/// A crash loop must not fill the device, and the first crashes of a loop say what the rest would.
let maxCrashFiles = 10

private let crashPrefix = "openlog-crash-"
private let crashSuffix = ".json"

final class CrashStore {
    private let dir: URL
    private let now: () -> Date
    private let fm = FileManager.default

    init(directory: URL, now: @escaping () -> Date = Date.init) {
        dir = directory
        self.now = now
    }

    func write(id: String, body: Data) {
        try? fm.createDirectory(at: dir, withIntermediateDirectories: true)
        // Keeps the earliest, and drops this one rather than one of them.
        if pending().count >= maxCrashFiles { return }
        let name = "\(crashPrefix)\(Int64(now().timeIntervalSince1970 * 1000))-\(id)\(crashSuffix)"
        // Atomic, so a process dying mid-write cannot leave half a report to be sent later.
        try? body.write(to: dir.appendingPathComponent(name), options: .atomic)
    }

    func pending() -> [URL] {
        let all = (try? fm.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil)) ?? []
        return all.filter { $0.lastPathComponent.hasPrefix(crashPrefix) && $0.lastPathComponent.hasSuffix(crashSuffix) }
            .sorted { $0.lastPathComponent < $1.lastPathComponent }
    }

    func remove(_ url: URL) { try? fm.removeItem(at: url) }
}

/// Sends what an earlier launch left behind, oldest first. A file goes once the server has answered for good, and stays
/// for the next launch when it said to wait or never answered.
func deliverCrashes(_ store: CrashStore, send: @escaping (Data, @escaping (Int) -> Void) -> Void, done: (() -> Void)? = nil) {
    var files = store.pending()[...]
    func next() {
        guard let file = files.popFirst(), let body = try? Data(contentsOf: file) else { done?(); return }
        send(body) { status in
            guard (200...299).contains(status) || ((400...499).contains(status) && status != 429) else { done?(); return }
            store.remove(file)
            next()
        }
    }
    next()
}

/// The name a signal goes by, since that is what someone searches for.
func signalName(_ signal: Int32?) -> String {
    switch signal {
    case SIGSEGV: return "SIGSEGV"
    case SIGABRT: return "SIGABRT"
    case SIGBUS: return "SIGBUS"
    case SIGILL: return "SIGILL"
    case SIGTRAP: return "SIGTRAP"
    case SIGFPE: return "SIGFPE"
    case SIGKILL: return "SIGKILL"
    case let s?: return "Signal \(s)"
    case nil: return "Crash"
    }
}

/// The session an earlier launch was in, so a report from MetricKit, which carries none, can be attached to it.
struct LastSession {
    var id: String
    var version: String

    private static let key = "openlog.rum.last_session"

    static func read(_ defaults: UserDefaults?) -> LastSession? {
        guard let parts = defaults?.string(forKey: key)?.components(separatedBy: "\n"), parts.count == 2 else { return nil }
        return LastSession(id: parts[0], version: parts[1])
    }

    func write(_ defaults: UserDefaults?) { defaults?.set("\(id)\n\(version)", forKey: LastSession.key) }
}

/// The instance the C exception handler reports to. That handler cannot capture anything.
var crashCaptureTarget: OpenLog?
var previousExceptionHandler: (@convention(c) (NSException) -> Void)?

func installExceptionHandler(_ sdk: OpenLog) {
    crashCaptureTarget = sdk
    previousExceptionHandler = NSGetUncaughtExceptionHandler()
    NSSetUncaughtExceptionHandler { exception in
        crashCaptureTarget?.record(exception: exception)
        // Never replaced, or the application's own crash tool would stop working silently.
        previousExceptionHandler?(exception)
    }
}

func removeExceptionHandler() {
    crashCaptureTarget = nil
    NSSetUncaughtExceptionHandler(previousExceptionHandler)
    previousExceptionHandler = nil
}

#if canImport(MetricKit) && (os(iOS) || os(macOS))
/// Receives what the operating system recorded about earlier launches.
@available(iOS 14.0, macOS 12.0, *)
final class CrashDiagnostics: NSObject, MXMetricManagerSubscriber {
    private weak var sdk: OpenLog?

    init(sdk: OpenLog) {
        self.sdk = sdk
        super.init()
        MXMetricManager.shared.add(self)
    }

    func stop() { MXMetricManager.shared.remove(self) }

    func didReceive(_ payloads: [MXDiagnosticPayload]) {
        for payload in payloads {
            for crash in payload.crashDiagnostics ?? [] {
                sdk?.record(
                    signal: crash.signal?.int32Value,
                    reason: crash.terminationReason ?? "",
                    stack: String(data: crash.callStackTree.jsonRepresentation(), encoding: .utf8) ?? "",
                    version: crash.metaData.applicationBuildVersion,
                    at: payload.timeStampEnd
                )
            }
        }
    }
}
#endif
