import XCTest
@testable import OpenLog

final class IdsTests: XCTestCase {
    func testIdsHaveTheShapesTheBackendRequires() {
        XCTAssertNotNil(newTraceID().range(of: "^[0-9a-f]{32}$", options: .regularExpression))
        XCTAssertNotNil(newSessionID().range(of: "^[0-9a-f]{32}$", options: .regularExpression))
        XCTAssertNotNil(newSpanID().range(of: "^[0-9a-f]{16}$", options: .regularExpression))
    }

    func testIdsAreNotReused() {
        let ids = Set((0..<200).map { _ in newTraceID() })
        // A repeat would mean two visits merged into one, or two spans claiming the same identity.
        XCTAssertEqual(ids.count, 200)
    }

    func testTruncateKeepsWholeCharacters() {
        XCTAssertEqual(truncate("aaaaaaaaa😀", 10).count, 10)
        XCTAssertEqual(truncate("short", 10), "short")
    }
}
