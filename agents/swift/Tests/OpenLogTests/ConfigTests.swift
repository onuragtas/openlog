import XCTest
@testable import OpenLog

final class ConfigTests: XCTestCase {
    private func valid(
        key: String = "olb_1a2b3c4d5e6f708192a3b4c5d6e7f809",
        endpoint: String = "https://ingest.example.com:4318",
        appID: String = "com.example.shop",
        sampleRate: Double = 1
    ) -> Options {
        Options(key: key, endpoint: endpoint, appID: appID, sampleRate: sampleRate)
    }

    func testDerivesTheTwoURLsWithoutATrailingSlash() throws {
        let c = try resolveConfig(valid(endpoint: "https://ingest.example.com:4318///"))
        XCTAssertEqual(c.rumURL, "https://ingest.example.com:4318/v1/rum")
        XCTAssertEqual(c.configURL, "https://ingest.example.com:4318/v1/rum/config")
    }

    func testRefusesOptionsTheServerWouldRefuse() {
        // Thrown rather than logged: an SDK that silently does nothing is found out weeks later.
        XCTAssertThrowsError(try resolveConfig(valid(key: "olk_an_ingest_license_key")))
        XCTAssertThrowsError(try resolveConfig(valid(endpoint: "ingest.example.com")))
        XCTAssertThrowsError(try resolveConfig(valid(appID: "   ")))
        XCTAssertThrowsError(try resolveConfig(valid(sampleRate: 0)))
        XCTAssertThrowsError(try resolveConfig(valid(sampleRate: 1.5)))
    }
}
