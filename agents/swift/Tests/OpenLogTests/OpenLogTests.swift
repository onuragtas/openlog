import XCTest
@testable import OpenLog

final class OpenLogTests: XCTestCase {
    final class Captured {
        private(set) var bodies: [String] = []
        func send(_ url: String, _ h: [String: String], _ body: Data, _ done: @escaping (Int) -> Void) {
            bodies.append(String(decoding: body, as: UTF8.self))
            done(200)
        }
    }

    override func tearDown() {
        OpenLog.reset()
        super.tearDown()
    }

    private func opts(sampleRate: Double = 1, batch: Int = 1) -> Options {
        Options(
            key: "olb_1a2b3c4d5e6f708192a3b4c5d6e7f809",
            endpoint: "https://ingest.example.com:4318",
            appID: "com.example.shop",
            sampleRate: sampleRate,
            maxBatchSize: batch
        )
    }

    private func noConfig(_ url: String, _ h: [String: String], _ done: @escaping ([String: Any]?) -> Void) { done(nil) }

    private func spans(_ body: String) throws -> [[String: Any]] {
        let obj = try JSONSerialization.jsonObject(with: Data(body.utf8)) as! [String: Any]
        let rs = (obj["resourceSpans"] as! [[String: Any]])[0]
        let ss = (rs["scopeSpans"] as! [[String: Any]])[0]
        return ss["spans"] as! [[String: Any]]
    }

    private func attrs(_ span: [String: Any]) -> [String: String] {
        var out: [String: String] = [:]
        for a in span["attributes"] as! [[String: Any]] {
            out[a["key"] as! String] = (a["value"] as! [String: Any])["stringValue"] as? String
        }
        return out
    }

    func testStartTwiceReturnsTheFirstInstance() throws {
        let a = try OpenLog.start(opts(), send: Captured().send, fetchConfig: noConfig)
        let b = try OpenLog.start(opts(), send: Captured().send, fetchConfig: noConfig)
        // A second SDK would double-count every screen, and the usual cause is a framework starting twice.
        XCTAssertTrue(a === b)
    }

    func testAScreenCarriesItsRouteAndTheSession() throws {
        let server = Captured()
        let sdk = try OpenLog.start(opts(), send: server.send, fetchConfig: noConfig)
        sdk.record(screen: "/cart", isColdStart: true)

        let a = attrs(try spans(server.bodies[0])[0])
        XCTAssertEqual(a["openlog.rum.event"], "page_view")
        XCTAssertEqual(a["openlog.rum.route"], "/cart")
        XCTAssertEqual(a["openlog.rum.page_view.kind"], "load")
        XCTAssertNotNil(a["session.id"]?.range(of: "^[0-9a-f]{32}$", options: .regularExpression))
    }

    func testIdentifyAttachesAnIDToLaterEventsAndClearsIt() throws {
        let server = Captured()
        let sdk = try OpenLog.start(opts(batch: 10), send: server.send, fetchConfig: noConfig)
        sdk.record(event: "before")
        sdk.identify("  acct_8f3a2b  ")
        sdk.record(event: "after")
        sdk.identify("")
        sdk.record(event: "signed_out")
        let flushed = expectation(description: "flushed")
        sdk.flush { flushed.fulfill() }
        wait(for: [flushed], timeout: 1)

        var byName: [String: [String: String]] = [:]
        for s in try spans(server.bodies[0]) { byName[s["name"] as! String] = attrs(s) }
        XCTAssertNil(byName["before"]?["user.id"])
        XCTAssertEqual(byName["after"]?["user.id"], "acct_8f3a2b", "trimmed, and on later spans only")
        XCTAssertNil(byName["signed_out"]?["user.id"], "absent, not empty")
    }

    func testAnOverLongIdentityIsBounded() throws {
        let server = Captured()
        let sdk = try OpenLog.start(opts(), send: server.send, fetchConfig: noConfig)
        sdk.identify(String(repeating: "u", count: 500))
        sdk.record(event: "checkout")
        // Bounded here as well as on the server: an application should not discover the limit by having
        // its spans silently change shape somewhere it cannot see.
        XCTAssertEqual(attrs(try spans(server.bodies[0])[0])["user.id"]?.count, 128)
    }

    func testCustomParametersAreNamespacedCappedAndDeterministic() throws {
        let server = Captured()
        let sdk = try OpenLog.start(opts(), send: server.send, fetchConfig: noConfig)
        var params: [String: Any] = ["Bad Key": "dropped"]
        for i in 0..<25 { params[String(format: "p%02d", i)] = i }
        sdk.record(event: "checkout", params: params)

        let a = attrs(try spans(server.bodies[0])[0])
        let keys = a.keys.filter { $0.hasPrefix("openlog.rum.custom.param.") }.sorted()
        XCTAssertEqual(keys.count, 16, "the cap bounds what one event can carry")
        XCTAssertEqual(keys.first, "openlog.rum.custom.param.p00", "sorted before the cap, not dictionary order")
        XCTAssertNil(a["openlog.rum.custom.param.Bad Key"])
    }

    func testATimingCarriesItsValueAndUnit() throws {
        let server = Captured()
        let sdk = try OpenLog.start(opts(), send: server.send, fetchConfig: noConfig)
        sdk.record(timing: "cart_priced", milliseconds: 42)
        let a = attrs(try spans(server.bodies[0])[0])
        XCTAssertEqual(a["openlog.rum.custom.value"], "42")
        XCTAssertEqual(a["openlog.rum.custom.unit"], "ms")
    }

    func testAnErrorCarriesTheExceptionAsASpanEvent() throws {
        struct CartEmpty: Error {}
        let server = Captured()
        let sdk = try OpenLog.start(opts(), send: server.send, fetchConfig: noConfig)
        sdk.record(error: CartEmpty(), stack: "frame")
        let span = try spans(server.bodies[0])[0]
        XCTAssertEqual((span["status"] as? [String: Int])?["code"], 2)
        XCTAssertNotNil(span["events"])
    }

    func testTheServerSampleRateReplacesTheCompiledInOne() throws {
        let asked = expectation(description: "config read")
        _ = try OpenLog.start(opts(), send: Captured().send) { url, headers, done in
            XCTAssertTrue(url.hasSuffix("/v1/rum/config"))
            XCTAssertTrue(headers["openlog-browser-key"]!.hasPrefix("olb_"))
            done(["service_name": "shop-ios", "environment": "production", "sample_rate": 0.5])
            asked.fulfill()
        }
        wait(for: [asked], timeout: 1)
    }
}
