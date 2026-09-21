import XCTest
@testable import OpenLog

final class OtlpTests: XCTestCase {
    private func attrs(_ encoded: [[String: Any]]) -> [String: String] {
        var out: [String: String] = [:]
        for a in encoded {
            let key = a["key"] as! String
            let value = (a["value"] as! [String: Any])["stringValue"] as! String
            out[key] = value
        }
        return out
    }

    func testEveryAttributeTravelsAsAString() {
        let got = attrs(encodeAttributes([("a", "x"), ("n", 42), ("b", true)]))
        XCTAssertEqual(got, ["a": "x", "n": "42", "b": "true"])
    }

    func testEmptyAndNilAttributesAreLeftOut() {
        XCTAssertEqual(attrs(encodeAttributes([("a", ""), ("b", nil), ("c", "x")])), ["c": "x"])
    }

    func testValuesAreBoundedAndAURLHasItsOwnBound() {
        let got = attrs(encodeAttributes([
            ("note", String(repeating: "x", count: Limits.maxAttrValueBytes * 2)),
            ("url.full", String(repeating: "y", count: Limits.maxURLBytes * 2)),
        ]))
        XCTAssertEqual(got["note"]?.count, Limits.maxAttrValueBytes)
        XCTAssertEqual(got["url.full"]?.count, Limits.maxURLBytes)
    }

    func testASpanCarriesItsEventKindAndNanosecondTimestamps() {
        let span = buildSpan(
            name: "screen /cart", event: RumEvent.screen,
            traceID: String(repeating: "a", count: 32), spanID: String(repeating: "b", count: 16),
            startMs: 1_700_000_000_000, durationMs: 250
        )
        XCTAssertEqual(span["startTimeUnixNano"] as? String, "1700000000000000000")
        XCTAssertEqual(span["endTimeUnixNano"] as? String, "1700000000250000000")
        XCTAssertEqual(attrs(span["attributes"] as! [[String: Any]])["openlog.rum.event"], "page_view")
    }

    func testAnErrorSpanCarriesTheExceptionAsASpanEvent() {
        let span = buildSpan(
            name: "error CartEmpty", event: RumEvent.error,
            traceID: String(repeating: "a", count: 32), spanID: String(repeating: "b", count: 16),
            startMs: 1_700_000_000_000, isError: true,
            exception: (type: "CartEmpty", message: "bad", stacktrace: "frame")
        )
        XCTAssertEqual((span["status"] as? [String: Int])?["code"], 2)
        let events = span["events"] as! [[String: Any]]
        XCTAssertEqual(attrs(events[0]["attributes"] as! [[String: Any]])["exception.type"], "CartEmpty")
    }

    func testThePayloadIsValidJSON() throws {
        let span = buildSpan(
            name: "screen /cart", event: RumEvent.screen,
            traceID: String(repeating: "a", count: 32), spanID: String(repeating: "b", count: 16),
            startMs: 1_700_000_000_000
        )
        let payload = buildPayload(spans: [span], resource: [("service.version", "4.2.1")])
        // Built by hand, so that it encodes at all is worth asserting rather than assuming.
        let data = try JSONSerialization.data(withJSONObject: payload)
        XCTAssertTrue(String(decoding: data, as: UTF8.self).contains("4.2.1"))
    }
}
