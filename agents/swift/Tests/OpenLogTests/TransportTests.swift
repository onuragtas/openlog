import XCTest
@testable import OpenLog

final class TransportTests: XCTestCase {
    /// Records what would have been sent and answers with a status the test chooses.
    final class FakeServer {
        var status: Int
        private(set) var headers: [[String: String]] = []
        private(set) var bodies: [String] = []
        init(_ status: Int) { self.status = status }

        func send(_ url: String, _ h: [String: String], _ body: Data, _ done: @escaping (Int) -> Void) {
            headers.append(h)
            bodies.append(String(decoding: body, as: UTF8.self))
            done(status)
        }
    }

    private func cfg(batch: Int = 3) throws -> ResolvedConfig {
        try resolveConfig(Options(
            key: "olb_1a2b3c4d5e6f708192a3b4c5d6e7f809",
            endpoint: "https://ingest.example.com:4318",
            appID: "com.example.shop",
            serviceVersion: "4.2.1",
            maxBatchSize: batch
        ))
    }

    private func span(_ name: String) -> [String: Any] {
        buildSpan(
            name: name, event: RumEvent.screen,
            traceID: String(repeating: "a", count: 32), spanID: String(repeating: "b", count: 16),
            startMs: 1_700_000_000_000
        )
    }

    private func spanNames(_ body: String) throws -> [String] {
        let obj = try JSONSerialization.jsonObject(with: Data(body.utf8)) as! [String: Any]
        let rs = (obj["resourceSpans"] as! [[String: Any]])[0]
        let ss = (rs["scopeSpans"] as! [[String: Any]])[0]
        return (ss["spans"] as! [[String: Any]]).map { $0["name"] as! String }
    }

    func testBuffersUntilTheBatchIsFullThenSendsOneRequest() throws {
        let server = FakeServer(200)
        let t = Transport(cfg: try cfg(batch: 3), send: server.send)
        t.add(span("a"))
        t.add(span("b"))
        XCTAssertTrue(server.bodies.isEmpty, "an incomplete batch is not sent")
        t.add(span("c"))
        XCTAssertEqual(server.bodies.count, 1)
        XCTAssertEqual(try spanNames(server.bodies[0]), ["a", "b", "c"])
    }

    func testSendsTheKeyAndTheApplicationID() throws {
        let server = FakeServer(200)
        let t = Transport(cfg: try cfg(batch: 1), send: server.send)
        t.add(span("a"))
        // A mobile key is scoped by both; a request that declares no application matches no allowlist.
        XCTAssertTrue(server.headers[0]["openlog-browser-key"]!.hasPrefix("olb_"))
        XCTAssertEqual(server.headers[0]["openlog-app-id"], "com.example.shop")
        XCTAssertEqual(server.headers[0]["content-type"], "application/json")
    }

    func testARefusedKeyStopsTheSDK() throws {
        let server = FakeServer(403)
        let t = Transport(cfg: try cfg(batch: 1), send: server.send)
        t.add(span("a"))
        XCTAssertTrue(t.refused)
        t.add(span("b"))
        // Nothing about the next attempt would differ.
        XCTAssertEqual(server.bodies.count, 1)
    }

    func testATemporaryRefusalKeepsTheBatch() throws {
        let server = FakeServer(503)
        let t = Transport(cfg: try cfg(batch: 1), send: server.send)
        t.add(span("a"))
        XCTAssertEqual(server.bodies.count, 1)

        server.status = 200
        let sent = expectation(description: "flushed")
        t.flush { sent.fulfill() }
        wait(for: [sent], timeout: 1)
        XCTAssertEqual(server.bodies.count, 2)
        XCTAssertEqual(try spanNames(server.bodies[1]), ["a"], "the spans were not lost")
    }

    func testTheBuildTravelsAsAResourceAttribute() throws {
        let server = FakeServer(200)
        let t = Transport(cfg: try cfg(batch: 1), send: server.send)
        t.add(span("a"))
        XCTAssertTrue(server.bodies[0].contains("service.version"))
        XCTAssertTrue(server.bodies[0].contains("4.2.1"))
    }
}
