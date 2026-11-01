import XCTest
@testable import OpenLog

final class VisitorTests: XCTestCase {
    private var defaults: UserDefaults!
    private var suite = ""
    private let shape = "^[0-9a-f]{32}$"

    override func setUp() {
        suite = "openlog.test.\(UUID().uuidString)"
        defaults = UserDefaults(suiteName: suite)
    }

    override func tearDown() {
        defaults.removePersistentDomain(forName: suite)
        OpenLog.reset()
    }

    private var stored: String? { defaults.string(forKey: "openlog.rum.visitor") }

    private func matchesShape(_ s: String) -> Bool { s.range(of: shape, options: .regularExpression) != nil }

    func testLeavesNothingOnTheDeviceUntilTheServerConfirms() {
        let v = Visitor(enabled: true, defaults: defaults)
        XCTAssertTrue(matchesShape(v.id))
        XCTAssertNil(stored)
        v.confirm()
        XCTAssertNotNil(stored)
    }

    func testHasNoIDWithoutAStore() {
        // A new one per launch would count every launch as a person.
        let v = Visitor(enabled: true, defaults: nil)
        XCTAssertEqual(v.id, "")
        v.confirm()
        v.setEnabled(true)
        XCTAssertEqual(v.id, "")
    }

    func testRecognisesTheSameVisitorOnTheNextLaunch() {
        let a = Visitor(enabled: true, defaults: defaults)
        a.confirm()
        XCTAssertEqual(Visitor(enabled: true, defaults: defaults).id, a.id)
    }

    func testRemovesWhatIsStoredWhenTheServerSaysTheKeyDoesNotRecognise() {
        Visitor(enabled: true, defaults: defaults).confirm()
        let b = Visitor(enabled: true, defaults: defaults)
        b.discard()
        XCTAssertEqual(b.id, "")
        XCTAssertNil(stored)
    }

    func testKeepsAWithdrawalAcrossLaunchesAndThroughAServerAnswer() {
        let a = Visitor(enabled: true, defaults: defaults)
        a.confirm()
        let before = a.id
        a.setEnabled(false)
        XCTAssertFalse(stored?.contains(before) ?? true)

        let reload = Visitor(enabled: true, defaults: defaults)
        XCTAssertEqual(reload.id, "", "the default must not undo a person saying no")
        reload.confirm()
        reload.discard()
        XCTAssertEqual(Visitor(enabled: true, defaults: defaults).id, "")
    }

    func testGivesANewIDWhenCollectionIsEnabledAgain() {
        let a = Visitor(enabled: true, defaults: defaults)
        a.confirm()
        let before = a.id
        a.setEnabled(false)
        a.setEnabled(true)
        XCTAssertTrue(matchesShape(a.id))
        XCTAssertNotEqual(a.id, before)
        XCTAssertEqual(Visitor(enabled: true, defaults: defaults).id, a.id)
    }

    func testKeepsTheEarlierIDWhenAnApplicationThatAsksFirstGetsConsent() {
        let consented = Visitor(enabled: true, defaults: defaults)
        consented.confirm()
        let v = Visitor(enabled: false, defaults: defaults)
        XCTAssertEqual(v.id, "")
        v.confirm()
        v.setEnabled(true)
        XCTAssertEqual(v.id, consented.id)
    }

    func testRotatesTheIDAfter13Months() {
        var now = Date(timeIntervalSince1970: 1_700_000_000)
        let a = Visitor(enabled: true, defaults: defaults, now: { now })
        a.confirm()
        now = now.addingTimeInterval(394 * 24 * 60 * 60)
        XCTAssertEqual(Visitor(enabled: true, defaults: defaults, now: { now }).id, a.id)
        now = now.addingTimeInterval(2 * 24 * 60 * 60)
        XCTAssertNotEqual(Visitor(enabled: true, defaults: defaults, now: { now }).id, a.id)
    }

    func testSurvivesACorruptValue() {
        defaults.set("nonsense", forKey: "openlog.rum.visitor")
        XCTAssertTrue(matchesShape(Visitor(enabled: true, defaults: defaults).id))
    }

    func testMatchesTheVectorEverySDKIsTestedAgainst() {
        // analytics.md §2. A different value here means one person is sampled differently per platform.
        XCTAssertEqual(visitorRoll("fedcba9876543210fedcba9876543210"), 777805509.0 / 4294967296.0)
    }

    func testDecidesSamplingByTheVisitorAndNotByTheSession() {
        let id = "fedcba9876543210fedcba9876543210"
        let place = visitorRoll(id)
        for _ in 0..<20 {
            XCTAssertTrue(SessionState(sampleRate: place + 0.01, roll: { visitorRoll(id) }).sampled)
            XCTAssertFalse(SessionState(sampleRate: place - 0.01, roll: { visitorRoll(id) }).sampled)
        }
    }

    func testNamesTheApplicationWhenItReadsTheConfig() throws {
        // A mobile key refuses a request that names no application.
        var sent: [String: String] = [:]
        let sdk = try OpenLog.start(
            Options(key: "olb_x", endpoint: "https://ingest.example.com", appID: "com.example", visitorDefaults: defaults),
            send: { _, _, _, done in done(200) },
            fetchConfig: { _, headers, done in
                sent = headers
                done(nil)
            }
        )
        XCTAssertEqual(sent["openlog-app-id"], "com.example")
        sdk.shutdown()
    }

    func testSetAnalyticsCollectionIsWhatAConsentDialogCalls() throws {
        let sdk = try OpenLog.start(
            Options(key: "olb_x", endpoint: "https://ingest.example.com", appID: "com.example",
                    analyticsCollection: false, visitorDefaults: defaults),
            send: { _, _, _, done in done(200) },
            fetchConfig: { _, _, done in done(["analytics_mode": "visitors"]) }
        )
        XCTAssertEqual(sdk.visitorID, "")
        sdk.setAnalyticsCollection(true)
        XCTAssertTrue(matchesShape(sdk.visitorID))
        XCTAssertNotNil(stored)
        sdk.setAnalyticsCollection(false)
        XCTAssertEqual(sdk.visitorID, "")
        sdk.shutdown()
    }
}
