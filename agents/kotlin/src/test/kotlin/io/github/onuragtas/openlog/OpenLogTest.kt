package io.github.onuragtas.openlog

import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

class OpenLogTest {
    private class Captured {
        val bodies = ArrayList<String>()
        fun send(url: String, h: Map<String, String>, body: String): Int {
            bodies.add(body)
            return 200
        }
    }

    @AfterTest
    fun tearDown() = OpenLog.reset()

    private fun opts(sampleRate: Double = 1.0, batch: Int = 1) = Options(
        key = "olb_1a2b3c4d5e6f708192a3b4c5d6e7f809",
        endpoint = "https://ingest.example.com:4318",
        appId = "com.example.shop",
        sampleRate = sampleRate,
        maxBatchSize = batch,
    )

    private val noConfig: ConfigFetch = { _, _ -> null }

    /**
     * The attributes of the span named [name], read back out of the encoded body.
     *
     * The body is split at span boundaries first. Matching across the whole body with a lazy pattern reads
     * one span's attributes into another's — which is exactly what this helper did at first, and it
     * reported a bug in identify() that was not there.
     */
    private fun attrsOf(body: String, name: String): Map<String, String> {
        val span = body.split("""{"traceId":""").firstOrNull { it.contains(""""name":"$name"""") }
            ?: error("no span named $name in $body")
        return Regex(""""key":"([^"]+)","value":\{"stringValue":"([^"]*)"\}""")
            .findAll(span).associate { it.groupValues[1] to it.groupValues[2] }
    }

    @Test
    fun `start twice returns the first instance`() {
        val a = OpenLog.start(opts(), Captured()::send, noConfig)
        val b = OpenLog.start(opts(), Captured()::send, noConfig)
        // A second SDK would double-count every screen, and the usual cause is a framework starting twice.
        assertTrue(a === b)
    }

    @Test
    fun `a screen carries its route and the session`() {
        val server = Captured()
        val sdk = OpenLog.start(opts(), server::send, noConfig)
        sdk.recordScreen("/cart", isColdStart = true)

        val a = attrsOf(server.bodies[0], "screen /cart")
        assertEquals("page_view", a["openlog.rum.event"])
        assertEquals("/cart", a["openlog.rum.route"])
        assertEquals("load", a["openlog.rum.page_view.kind"])
        assertTrue(Regex("^[0-9a-f]{32}$").matches(a["session.id"]!!))
    }

    @Test
    fun `identify attaches an id to later events and clears it`() {
        val server = Captured()
        val sdk = OpenLog.start(opts(batch = 10), server::send, noConfig)
        sdk.recordEvent("before")
        sdk.identify("  acct_8f3a2b  ")
        sdk.recordEvent("after")
        sdk.identify("")
        sdk.recordEvent("signed_out")
        sdk.flush()

        val body = server.bodies[0]
        assertNull(attrsOf(body, "before")["user.id"])
        assertEquals("acct_8f3a2b", attrsOf(body, "after")["user.id"], "trimmed, and on later spans only")
        assertNull(attrsOf(body, "signed_out")["user.id"], "absent, not empty")
        assertEquals(
            1, Regex(""""key":"user\.id"""").findAll(body).count(),
            "exactly one of the three spans carries an identity",
        )
    }

    @Test
    fun `an over-long identity is bounded`() {
        val server = Captured()
        val sdk = OpenLog.start(opts(), server::send, noConfig)
        sdk.identify("u".repeat(500))
        sdk.recordEvent("checkout")
        // Bounded here as well as on the server: an application should not discover the limit by having
        // its spans silently change shape somewhere it cannot see.
        assertEquals(128, attrsOf(server.bodies[0], "checkout")["user.id"]!!.length)
    }

    @Test
    fun `custom parameters are namespaced capped and deterministic`() {
        val server = Captured()
        val sdk = OpenLog.start(opts(), server::send, noConfig)
        val params = HashMap<String, Any?>()
        for (i in 0 until 25) params[String.format("p%02d", i)] = i
        params["Bad Key"] = "dropped"
        sdk.recordEvent("checkout", params)

        val a = attrsOf(server.bodies[0], "checkout")
        val keys = a.keys.filter { it.startsWith("openlog.rum.custom.param.") }.sorted()
        assertEquals(16, keys.size, "the cap bounds what one event can carry")
        assertEquals("openlog.rum.custom.param.p00", keys.first(), "sorted before the cap, not map order")
        assertNull(a["openlog.rum.custom.param.Bad Key"])
    }

    @Test
    fun `a timing carries its value and unit`() {
        val server = Captured()
        val sdk = OpenLog.start(opts(), server::send, noConfig)
        sdk.recordTiming("cart_priced", 42)
        val a = attrsOf(server.bodies[0], "cart_priced")
        assertEquals("42", a["openlog.rum.custom.value"])
        assertEquals("ms", a["openlog.rum.custom.unit"])
    }

    @Test
    fun `an error carries the exception as a span event`() {
        val server = Captured()
        val sdk = OpenLog.start(opts(), server::send, noConfig)
        sdk.recordError(IllegalStateException("cart is empty"))
        assertTrue(server.bodies[0].contains("exception.type"))
        assertTrue(server.bodies[0].contains(""""status":{"code":2}"""))
    }

    @Test
    fun `the config response is read without a JSON parser`() {
        val got = parseConfigFields("""{"service_name":"shop-android","environment":"production","sample_rate":0.5}""")
        assertEquals("shop-android", got["service_name"])
        assertEquals("production", got["environment"])
        assertEquals(0.5, got["sample_rate"])
        // A field that is absent is simply absent, which is the same as the call having failed.
        assertNull(parseConfigFields("""{"service_name":"x"}""")["sample_rate"])
        assertNull(parseConfigFields("""{"sample_rate":"half"}""")["sample_rate"])
    }
}
