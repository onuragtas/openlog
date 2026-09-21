package io.github.onuragtas.openlog

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull
import kotlin.test.assertNull

class OtlpTest {
    private fun attrs(encoded: Any?): Map<String, String> {
        @Suppress("UNCHECKED_CAST")
        val list = encoded as List<Map<String, Any>>
        return list.associate {
            @Suppress("UNCHECKED_CAST")
            val v = it["value"] as Map<String, Any>
            it["key"] as String to v["stringValue"] as String
        }
    }

    @Test
    fun `every attribute travels as a string`() {
        val got = attrs(encodeAttributes(listOf("a" to "x", "n" to 42, "b" to true)))
        assertEquals(mapOf("a" to "x", "n" to "42", "b" to "true"), got)
    }

    @Test
    fun `empty and null attributes are left out rather than sent blank`() {
        assertEquals(mapOf("c" to "x"), attrs(encodeAttributes(listOf("a" to "", "b" to null, "c" to "x"))))
    }

    @Test
    fun `values are bounded and a URL has its own bound`() {
        val got = attrs(
            encodeAttributes(
                listOf(
                    "note" to "x".repeat(Limits.MAX_ATTR_VALUE_BYTES * 2),
                    "url.full" to "y".repeat(Limits.MAX_URL_BYTES * 2),
                )
            )
        )
        assertEquals(Limits.MAX_ATTR_VALUE_BYTES, got["note"]!!.length)
        assertEquals(Limits.MAX_URL_BYTES, got["url.full"]!!.length)
    }

    @Test
    fun `a span carries its event kind and nanosecond timestamps`() {
        val span = buildSpan(
            name = "screen /cart", event = RumEvent.SCREEN,
            traceId = "a".repeat(32), spanId = "b".repeat(16),
            startMs = 1_700_000_000_000, durationMs = 250,
        )
        assertEquals("1700000000000000000", span["startTimeUnixNano"])
        assertEquals("1700000000250000000", span["endTimeUnixNano"])
        assertEquals("page_view", attrs(span["attributes"])["openlog.rum.event"])
        assertNull(span["parentSpanId"], "a screen has no parent")
    }

    @Test
    fun `an error span carries the exception as a span event`() {
        val span = buildSpan(
            name = "error CartEmpty", event = RumEvent.ERROR,
            traceId = "a".repeat(32), spanId = "b".repeat(16),
            startMs = 1_700_000_000_000, isError = true,
            exception = Triple("CartEmpty", "bad", "frame"),
        )
        assertEquals(mapOf("code" to 2), span["status"])
        assertNotNull(span["events"])
    }

    @Test
    fun `the payload encodes`() {
        val span = buildSpan(
            name = "screen /cart", event = RumEvent.SCREEN,
            traceId = "a".repeat(32), spanId = "b".repeat(16), startMs = 1_700_000_000_000,
        )
        val json = jsonEncode(buildPayload(listOf(span), listOf("service.version" to "4.2.1")))
        assert(json.contains("4.2.1")) { "the build must reach the resource" }
        assert(json.startsWith("""{"resourceSpans":[""")) { json.take(40) }
    }
}
