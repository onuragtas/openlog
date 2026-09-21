package io.github.onuragtas.openlog

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotEquals
import kotlin.test.assertTrue

class SessionTest {
    @Test
    fun `a quiet session expires and a new one begins`() {
        var now = 1_700_000_000_000L
        val s = SessionState(1.0) { now }
        val first = s.id

        now += 29 * 60 * 1000
        assertFalse(s.touch(), "still the same visit")
        assertEquals(first, s.id)

        now += 31 * 60 * 1000
        assertTrue(s.touch(), "nothing happened for over half an hour")
        assertNotEquals(first, s.id)
    }

    @Test
    fun `a busy session still ends at the cap`() {
        var now = 1_700_000_000_000L
        val s = SessionState(1.0) { now }
        val first = s.id
        // Active the whole time: the idle timeout never fires, and without the cap this would be one
        // session for as long as the screen stays open.
        repeat(24) {
            now += 10 * 60 * 1000
            s.touch()
        }
        assertNotEquals(first, s.id)
    }

    @Test
    fun `a new screen is a new trace in the same session`() {
        val s = SessionState(1.0)
        val session = s.id
        val trace = s.traceId
        s.newScreen()
        assertNotEquals(trace, s.traceId)
        assertEquals(session, s.id)
    }

    @Test
    fun `sampling is decided once for the whole session`() {
        assertTrue(SessionState(1.0).shouldSend)
        assertFalse(SessionState(0.0).shouldSend)
    }
}
