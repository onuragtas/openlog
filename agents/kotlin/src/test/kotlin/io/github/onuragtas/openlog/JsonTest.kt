package io.github.onuragtas.openlog

import kotlin.test.Test
import kotlin.test.assertEquals

class JsonTest {
    @Test
    fun `escaping is what a hand-written writer must get right`() {
        assertEquals("\"a\\\"b\"", jsonEncode("a\"b"))
        assertEquals("\"a\\\\b\"", jsonEncode("a\\b"))
        assertEquals("\"line\\nbreak\"", jsonEncode("line\nbreak"))
        // A control character arriving in an error message would otherwise make the whole batch unparseable.
        assertEquals("\"\\u0007\"", jsonEncode("\u0007"))
    }

    @Test
    fun `nested structures encode as JSON`() {
        val encoded = jsonEncode(mapOf("a" to listOf(1, true, null), "b" to mapOf("c" to "d")))
        assertEquals("""{"a":[1,true,null],"b":{"c":"d"}}""", encoded)
    }

    @Test
    fun `values JSON cannot represent become null rather than breaking the document`() {
        assertEquals("null", jsonEncode(Double.NaN))
        assertEquals("null", jsonEncode(Double.POSITIVE_INFINITY))
    }
}
