package io.github.onuragtas.openlog

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

class TransportTest {
    /** Records what would have been sent and answers with a status the test chooses. */
    private class FakeServer(var status: Int) {
        val headers = ArrayList<Map<String, String>>()
        val bodies = ArrayList<String>()
        fun send(url: String, h: Map<String, String>, body: String): Int {
            headers.add(h)
            bodies.add(body)
            return status
        }
    }

    private fun cfg(batch: Int = 3) = resolveConfig(
        Options(
            key = "olb_1a2b3c4d5e6f708192a3b4c5d6e7f809",
            endpoint = "https://ingest.example.com:4318",
            appId = "com.example.shop",
            serviceVersion = "4.2.1",
            maxBatchSize = batch,
        )
    )

    private fun span(name: String) = buildSpan(
        name = name, event = RumEvent.SCREEN,
        traceId = "a".repeat(32), spanId = "b".repeat(16), startMs = 1_700_000_000_000,
    )

    private fun spanNames(body: String): List<String> =
        Regex(""""name":"([^"]+)"""").findAll(body).map { it.groupValues[1] }.toList()

    @Test
    fun `buffers until the batch is full then sends one request with every span`() {
        val server = FakeServer(200)
        val t = Transport(cfg(batch = 3), server::send)
        t.add(span("a"))
        t.add(span("b"))
        assertTrue(server.bodies.isEmpty(), "an incomplete batch is not sent")
        t.add(span("c"))
        assertEquals(1, server.bodies.size)
        assertEquals(listOf("a", "b", "c"), spanNames(server.bodies[0]))
        t.stop()
    }

    @Test
    fun `sends the key and the application id`() {
        val server = FakeServer(200)
        val t = Transport(cfg(batch = 1), server::send)
        t.add(span("a"))
        // A mobile key is scoped by both; a request that declares no application matches no allowlist.
        assertTrue(server.headers[0]["openlog-browser-key"]!!.startsWith("olb_"))
        assertEquals("com.example.shop", server.headers[0]["openlog-app-id"])
        assertEquals("application/json", server.headers[0]["content-type"])
        t.stop()
    }

    @Test
    fun `a refused key stops the SDK instead of retrying a permanent answer`() {
        val server = FakeServer(403)
        val t = Transport(cfg(batch = 1), server::send)
        t.add(span("a"))
        assertTrue(t.refused)
        t.add(span("b"))
        assertEquals(1, server.bodies.size, "nothing about the next attempt would differ")
        t.stop()
    }

    @Test
    fun `a temporary refusal keeps the batch for the next flush`() {
        val server = FakeServer(503)
        val t = Transport(cfg(batch = 1), server::send)
        t.add(span("a"))
        assertEquals(1, server.bodies.size)

        server.status = 200
        t.flush()
        assertEquals(2, server.bodies.size)
        assertEquals(listOf("a"), spanNames(server.bodies[1]), "the spans were not lost")
        t.stop()
    }

    @Test
    fun `the build travels as a resource attribute`() {
        val server = FakeServer(200)
        val t = Transport(cfg(batch = 1), server::send)
        t.add(span("a"))
        assertTrue(server.bodies[0].contains("service.version"))
        assertTrue(server.bodies[0].contains("4.2.1"))
        t.stop()
    }
}
