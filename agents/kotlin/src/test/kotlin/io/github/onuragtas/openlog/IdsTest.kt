package io.github.onuragtas.openlog

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

class IdsTest {
    @Test
    fun `ids have the shapes the backend requires`() {
        assertTrue(Regex("^[0-9a-f]{32}$").matches(newTraceId()))
        assertTrue(Regex("^[0-9a-f]{32}$").matches(newSessionId()))
        assertTrue(Regex("^[0-9a-f]{16}$").matches(newSpanId()))
    }

    @Test
    fun `ids are not reused`() {
        val ids = (0 until 200).map { newTraceId() }.toSet()
        // A repeat would mean two visits merged into one, or two spans claiming the same identity.
        assertEquals(200, ids.size)
    }

    @Test
    fun `truncate keeps what fits`() {
        assertEquals("short", truncate("short", 10))
        assertEquals(10, truncate("a".repeat(50), 10).length)
    }
}
