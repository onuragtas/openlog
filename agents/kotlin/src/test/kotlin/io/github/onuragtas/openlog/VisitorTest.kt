package io.github.onuragtas.openlog

import java.io.File
import java.nio.file.Files
import kotlin.test.AfterTest
import kotlin.test.BeforeTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotEquals
import kotlin.test.assertTrue

class VisitorTest {
    private lateinit var dir: File
    private val file get() = File(dir, "openlog_visitor")
    private val shape = Regex("^[0-9a-f]{32}$")

    @BeforeTest
    fun setUp() {
        dir = Files.createTempDirectory("openlog_visitor").toFile()
    }

    @AfterTest
    fun tearDown() {
        dir.deleteRecursively()
        OpenLog.reset()
    }

    private fun visitor(enabled: Boolean = true, now: () -> Long = System::currentTimeMillis) =
        Visitor(enabled, dir.path, now)

    @Test
    fun `leaves nothing on the device until the server confirms visitors mode`() {
        val v = visitor()
        assertTrue(shape.matches(v.id))
        assertFalse(file.exists())
        v.confirm()
        assertTrue(file.exists())
    }

    @Test
    fun `has no id without a directory`() {
        // A new one per launch would count every launch as a person.
        val v = Visitor(true, null)
        assertEquals("", v.id)
        v.confirm()
        v.setEnabled(true)
        assertEquals("", v.id)
    }

    @Test
    fun `recognises the same visitor on the next launch`() {
        val a = visitor().also { it.confirm() }
        assertEquals(a.id, visitor().id)
    }

    @Test
    fun `removes what is stored when the server says the key does not recognise visitors`() {
        visitor().confirm()
        val b = visitor().also { it.discard() }
        assertEquals("", b.id)
        assertFalse(file.exists())
    }

    @Test
    fun `keeps a withdrawal across launches and through a server answer`() {
        val a = visitor().also { it.confirm() }
        val before = a.id
        a.setEnabled(false)
        assertFalse(file.readText().contains(before))

        val reload = visitor()
        assertEquals("", reload.id, "the default must not undo a person saying no")
        reload.confirm()
        reload.discard()
        assertEquals("", visitor().id)
    }

    @Test
    fun `gives a new id when collection is enabled again`() {
        val a = visitor().also { it.confirm() }
        val before = a.id
        a.setEnabled(false)
        a.setEnabled(true)
        assertTrue(shape.matches(a.id))
        assertNotEquals(before, a.id)
        assertEquals(a.id, visitor().id)
    }

    @Test
    fun `keeps the earlier id when an application that asks first gets consent`() {
        val consented = visitor().also { it.confirm() }
        val v = visitor(enabled = false)
        assertEquals("", v.id)
        v.confirm()
        v.setEnabled(true)
        assertEquals(consented.id, v.id)
    }

    @Test
    fun `rotates the id after 13 months`() {
        var now = 1_700_000_000_000L
        val a = visitor { now }.also { it.confirm() }
        now += 394L * 24 * 60 * 60 * 1000
        assertEquals(a.id, visitor { now }.id)
        now += 2L * 24 * 60 * 60 * 1000
        assertNotEquals(a.id, visitor { now }.id)
    }

    @Test
    fun `survives a corrupt file and a directory it cannot write`() {
        file.writeText("nonsense")
        assertTrue(shape.matches(visitor().id))
        val v = Visitor(true, File(dir, "does/not/exist").path)
        v.confirm()
        v.setEnabled(false)
        assertEquals("", v.id)
    }

    @Test
    fun `matches the vector every SDK is tested against`() {
        // analytics.md §2. A different value here means one person is sampled differently per platform.
        assertEquals(777805509 / 4294967296.0, visitorRoll("fedcba9876543210fedcba9876543210"))
    }

    @Test
    fun `decides sampling by the visitor and not by the session`() {
        val id = "fedcba9876543210fedcba9876543210"
        val place = visitorRoll(id)
        repeat(20) {
            assertTrue(SessionState(place + 0.01, roll = { visitorRoll(id) }).sampled)
            assertFalse(SessionState(place - 0.01, roll = { visitorRoll(id) }).sampled)
        }
    }

    @Test
    fun `reads the analytics mode out of the config response`() {
        assertEquals("visitors", parseConfigFields("""{"sample_rate":1,"analytics_mode":"visitors"}""")["analytics_mode"])
    }

    @Test
    fun `names the application when it reads the config, or a mobile key refuses it`() {
        val answered = java.util.concurrent.CountDownLatch(1)
        var sent: Map<String, String> = emptyMap()
        val sdk = OpenLog.start(
            Options(key = "olb_x", endpoint = "https://ingest.example.com", appId = "com.example"),
            send = { _, _, _ -> 200 },
            fetchConfig = { _, headers ->
                sent = headers
                answered.countDown()
                null
            },
        )
        answered.await()
        assertEquals("com.example", sent["openlog-app-id"])
        sdk.shutdown()
    }

    @Test
    fun `setAnalyticsCollection is what a consent dialog calls`() {
        val answered = java.util.concurrent.CountDownLatch(1)
        val sdk = OpenLog.start(
            Options(
                key = "olb_x", endpoint = "https://ingest.example.com", appId = "com.example",
                analyticsCollection = false, visitorDirectory = dir.path,
            ),
            send = { _, _, _ -> 200 },
            fetchConfig = { _, _ -> mapOf<String, Any?>("analytics_mode" to "visitors").also { answered.countDown() } },
        )
        answered.await()
        assertEquals("", sdk.visitorId)
        sdk.setAnalyticsCollection(true)
        assertTrue(shape.matches(sdk.visitorId))
        sdk.setAnalyticsCollection(false)
        assertEquals("", sdk.visitorId)
        sdk.shutdown()
    }
}
