import XCTest
@testable import OpenLog

final class SessionTests: XCTestCase {
    func testAQuietSessionExpiresAndANewOneBegins() {
        var now = Date(timeIntervalSince1970: 1_700_000_000)
        let s = SessionState(sampleRate: 1, now: { now })
        let first = s.id

        now = now.addingTimeInterval(29 * 60)
        XCTAssertFalse(s.touch(), "still the same visit")
        XCTAssertEqual(s.id, first)

        now = now.addingTimeInterval(31 * 60)
        XCTAssertTrue(s.touch(), "nothing happened for over half an hour")
        XCTAssertNotEqual(s.id, first)
    }

    func testABusySessionStillEndsAtTheCap() {
        var now = Date(timeIntervalSince1970: 1_700_000_000)
        let s = SessionState(sampleRate: 1, now: { now })
        let first = s.id
        // Active the whole time: the idle timeout never fires, and without the cap this would be one
        // session for as long as the screen stays open.
        for _ in 0..<24 {
            now = now.addingTimeInterval(10 * 60)
            s.touch()
        }
        XCTAssertNotEqual(s.id, first)
    }

    func testANewScreenIsANewTraceInTheSameSession() {
        let s = SessionState(sampleRate: 1)
        let session = s.id
        let trace = s.traceID
        s.newScreen()
        XCTAssertNotEqual(s.traceID, trace)
        XCTAssertEqual(s.id, session)
    }

    func testSamplingIsDecidedOnceForTheWholeSession() {
        XCTAssertTrue(SessionState(sampleRate: 1).shouldSend)
        XCTAssertFalse(SessionState(sampleRate: 0).shouldSend)
    }
}
