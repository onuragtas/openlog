import XCTest
@testable import OpenLog

final class CrashTests: XCTestCase {
    private var dir: URL!
    private var defaults: UserDefaults!
    private var suite = ""

    override func setUp() {
        dir = FileManager.default.temporaryDirectory.appendingPathComponent("openlog-crash-\(UUID().uuidString)")
        suite = "openlog.test.\(UUID().uuidString)"
        defaults = UserDefaults(suiteName: suite)
    }

    override func tearDown() {
        OpenLog.reset()
        try? FileManager.default.removeItem(at: dir)
        defaults.removePersistentDomain(forName: suite)
    }

    private func options(capture: Bool = true, version: String = "1.2.3") -> Options {
        Options(key: "olb_x", endpoint: "https://ingest.example.com", appID: "com.example", serviceVersion: version,
                visitorDefaults: defaults, captureCrashes: capture, crashDirectory: dir)
    }

    private func start(capture: Bool = true, version: String = "1.2.3", send: @escaping HTTPSend = { _, _, _, done in done(200) }) throws -> OpenLog {
        try OpenLog.start(options(capture: capture, version: version), send: send, fetchConfig: { _, _, done in done(nil) })
    }

    private func report(_ url: URL) -> String { String(decoding: (try? Data(contentsOf: url)) ?? Data(), as: UTF8.self) }

    func testAnExceptionWritesOneReportAndSendsNothing() throws {
        var sent = 0
        let sdk = try start(send: { _, _, _, done in sent += 1; done(200) })
        sdk.record(screen: "/cart")
        sdk.record(exception: NSException(name: .internalInconsistencyException, reason: "boom", userInfo: nil))
        XCTAssertEqual(sent, 0)
        let files = CrashStore(directory: dir).pending()
        XCTAssertEqual(files.count, 1)
        let body = report(files[0])
        XCTAssertTrue(body.contains("uncaught_exception"), body)
        XCTAssertTrue(body.contains("NSInternalInconsistencyException"), body)
        XCTAssertTrue(body.contains("boom"), body)
        XCTAssertTrue(body.contains("/cart"), body)
        XCTAssertTrue(body.contains(sdk.sessionID), body)
        XCTAssertTrue(body.contains("1.2.3"), body)
        sdk.shutdown()
    }

    func testTheNextLaunchDeliversItAndRemovesIt() throws {
        let crashed = try start()
        let session = crashed.sessionID
        crashed.record(exception: NSException(name: .genericException, reason: "boom", userInfo: nil))
        crashed.shutdown()

        var bodies: [String] = []
        var headers: [String: String] = [:]
        let sdk = try start(send: { _, h, b, done in
            bodies.append(String(decoding: b, as: UTF8.self))
            headers = h
            done(200)
        })
        XCTAssertEqual(bodies.count, 1)
        XCTAssertTrue(bodies[0].contains(session), "not the session of this launch")
        XCTAssertNotEqual(sdk.sessionID, session)
        XCTAssertEqual(headers["openlog-app-id"], "com.example")
        XCTAssertTrue(CrashStore(directory: dir).pending().isEmpty)
        sdk.shutdown()
    }

    func testASignalIsAttachedToTheLastSessionAndItsBuild() throws {
        let earlier = try start(version: "1.0.0")
        let session = earlier.sessionID
        earlier.record(screen: "/x")
        earlier.shutdown()

        var body = ""
        let sdk = try start(version: "2.0.0", send: { _, _, b, done in body = String(decoding: b, as: UTF8.self); done(200) })
        sdk.record(signal: SIGSEGV, reason: "Namespace SIGNAL", stack: "{\"callStacks\":[]}", version: "1.0.0",
                   at: Date(timeIntervalSince1970: 1_700_000_000))
        // The process is alive this time, so it goes straight out.
        XCTAssertTrue(CrashStore(directory: dir).pending().isEmpty)
        XCTAssertTrue(body.contains("SIGSEGV"), body)
        XCTAssertTrue(body.contains("\"signal\""), body)
        XCTAssertTrue(body.contains(session), "the session that crashed: \(body)")
        XCTAssertTrue(body.contains("1.0.0"), "the build that crashed, not this one: \(body)")
        XCTAssertFalse(body.contains("2.0.0"), body)
        XCTAssertTrue(body.contains("1700000000000000000"), "when it crashed: \(body)")
        XCTAssertFalse(body.contains("session_unknown"), body)
        sdk.shutdown()
    }

    func testASignalWithNoKnownSessionSaysSo() throws {
        var body = ""
        let sdk = try start(send: { _, _, b, done in body = String(decoding: b, as: UTF8.self); done(200) })
        sdk.record(signal: SIGABRT, reason: "", stack: "", version: "0.9.0", at: Date())
        XCTAssertTrue(body.contains("session_unknown"), body)
        XCTAssertFalse(body.contains(sdk.sessionID), "this launch did not crash: \(body)")
        sdk.shutdown()
    }

    func testKeepsAReportUntilTheServerHasAnsweredForGood() {
        let store = CrashStore(directory: dir)
        store.write(id: "a", body: Data("{}".utf8))
        for status in [-1, 429, 503] {
            deliverCrashes(store, send: { _, done in done(status) })
            XCTAssertEqual(store.pending().count, 1, "after \(status)")
        }
        deliverCrashes(store, send: { _, done in done(200) })
        XCTAssertTrue(store.pending().isEmpty)
        store.write(id: "b", body: Data("{}".utf8))
        deliverCrashes(store, send: { _, done in done(400) })
        XCTAssertTrue(store.pending().isEmpty, "a body the server refuses will not improve")
    }

    func testACrashLoopCannotFillTheDevice() {
        var now = 1000.0
        let store = CrashStore(directory: dir, now: { now += 1; return Date(timeIntervalSince1970: now) })
        for i in 1...(maxCrashFiles + 5) { store.write(id: "id\(i)", body: Data("report \(i)".utf8)) }
        let files = store.pending()
        XCTAssertEqual(files.count, maxCrashFiles)
        XCTAssertEqual(report(files[0]), "report 1")
    }

    func testABuildSaysItCanReportCrashesOnlyWhenItCan() throws {
        var body = ""
        let sdk = try start(send: { _, _, b, done in body = String(decoding: b, as: UTF8.self); done(200) })
        sdk.record(screen: "/x")
        sdk.flush()
        XCTAssertTrue(body.contains("openlog.rum.crash.capture"), body)
        sdk.shutdown()

        var plain = ""
        let other = try start(capture: false, send: { _, _, b, done in plain = String(decoding: b, as: UTF8.self); done(200) })
        other.record(screen: "/x")
        other.flush()
        XCTAssertFalse(plain.contains("openlog.rum.crash.capture"), plain)
        other.shutdown()
    }

    func testPutsTheHandlerBackOnShutdown() throws {
        let sdk = try start()
        XCTAssertNotNil(NSGetUncaughtExceptionHandler())
        sdk.shutdown()
        XCTAssertNil(crashCaptureTarget)
    }

    func testFlattensTheThreadThatCrashed() {
        let tree = """
        {"callStackPerThread":true,"callStacks":[
          {"threadAttributed":false,"callStackRootFrames":[{"binaryUUID":"AAAAAAAA-0000-0000-0000-000000000000","offsetIntoBinaryTextSegment":1,"binaryName":"Other"}]},
          {"threadAttributed":true,"callStackRootFrames":[{"binaryUUID":"A1B2C3D4-E5F6-0718-A1B2-C3D4E5F60718","offsetIntoBinaryTextSegment":4096,"sampleCount":1,"binaryName":"My App",
            "subFrames":[{"binaryUUID":"FFFFFFFF-0000-0000-0000-000000000000","offsetIntoBinaryTextSegment":16,"binaryName":"libsystem_c.dylib"}]}]}]}
        """
        XCTAssertEqual(
            flattenCallStackTree(Data(tree.utf8)),
            "#00 My_App A1B2C3D4-E5F6-0718-A1B2-C3D4E5F60718 +0x1000\n#01 libsystem_c.dylib FFFFFFFF-0000-0000-0000-000000000000 +0x10"
        )
    }

    func testAStackTreeThatCannotBeReadComesBackAsItWas() {
        XCTAssertEqual(flattenCallStackTree(Data("not json".utf8)), "not json")
        XCTAssertEqual(flattenCallStackTree(Data("{\"callStacks\":[]}".utf8)), "{\"callStacks\":[]}")
    }

    func testAStackIsNotCutToTheLimitOfAnAttribute() {
        let long = String(repeating: "x", count: 9000)
        let encoded = encodeAttributes([("exception.stacktrace", long), ("other", long)])
        XCTAssertEqual(((encoded[0]["value"] as? [String: Any])?["stringValue"] as? String)?.count, 9000)
        XCTAssertEqual(((encoded[1]["value"] as? [String: Any])?["stringValue"] as? String)?.count, Limits.maxAttrValueBytes)
    }

    func testABuildNumberIsReportedAsTheVersionOfThatBuild() throws {
        let first = try start(version: "1.2.3")
        XCTAssertEqual(first.version(ofBuild: "41", currentBuild: "41"), "1.2.3")
        OpenLog.reset()
        let second = try start(version: "1.2.4")
        XCTAssertEqual(second.version(ofBuild: "41", currentBuild: "42"), "1.2.3", "an earlier build is what the last session ran")
    }
}
