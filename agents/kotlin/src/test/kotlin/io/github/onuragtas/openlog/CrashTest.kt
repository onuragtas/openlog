package io.github.onuragtas.openlog

import java.io.File
import java.nio.file.Files
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlin.test.AfterTest
import kotlin.test.BeforeTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertSame
import kotlin.test.assertTrue

class CrashTest {
    private lateinit var dir: File
    private var original: Thread.UncaughtExceptionHandler? = null

    @BeforeTest
    fun setUp() {
        dir = Files.createTempDirectory("openlog_crash").toFile()
        original = Thread.getDefaultUncaughtExceptionHandler()
    }

    @AfterTest
    fun tearDown() {
        Thread.setDefaultUncaughtExceptionHandler(original)
        OpenLog.reset()
        dir.deleteRecursively()
    }

    private fun options(capture: Boolean = true) = Options(
        key = "olb_x", endpoint = "https://ingest.example.com", appId = "com.example", serviceVersion = "1.2.3",
        captureCrashes = capture, crashDirectory = dir.path,
    )

    private fun start(capture: Boolean = true, send: HttpSend = { _, _, _ -> 200 }) =
        OpenLog.start(options(capture), send = send, fetchConfig = { _, _ -> null })

    private fun die(thread: Thread = Thread.currentThread(), error: Throwable = IllegalStateException("boom")) {
        Thread.getDefaultUncaughtExceptionHandler()!!.uncaughtException(thread, error)
    }

    @Test
    fun `leaves the handler alone unless asked`() {
        val before = Thread.getDefaultUncaughtExceptionHandler()
        val sdk = start(capture = false)
        assertSame(before, Thread.getDefaultUncaughtExceptionHandler())
        sdk.shutdown()
    }

    @Test
    fun `writes one report and sends nothing from the dying process`() {
        var sent = 0
        val sdk = start(send = { _, _, _ -> sent++; 200 })
        val before = sent
        sdk.recordScreen("/cart")
        die()
        assertEquals(before, sent)
        val files = CrashStore(dir.path).pending()
        assertEquals(1, files.size)
        val body = files[0].readText()
        assertTrue(body.contains("uncaught_exception"), body)
        assertTrue(body.contains("IllegalStateException"), body)
        assertTrue(body.contains("boom"), body)
        assertTrue(body.contains("/cart"), "the screen it died on: $body")
        assertTrue(body.contains(sdk.sessionId), "the session that crashed: $body")
        assertTrue(body.contains("1.2.3"), "the build that crashed: $body")
        sdk.shutdown()
    }

    @Test
    fun `never replaces the handler that was there, because that would silently stop another crash tool`() {
        var called: Throwable? = null
        val mine = Thread.UncaughtExceptionHandler { _, e -> called = e }
        Thread.setDefaultUncaughtExceptionHandler(mine)
        val sdk = start()
        val error = IllegalStateException("boom")
        die(error = error)
        assertSame(error, called)
        sdk.shutdown()
        assertSame(mine, Thread.getDefaultUncaughtExceptionHandler(), "shutdown puts it back")
    }

    @Test
    fun `a failure while recording still reaches the previous handler`() {
        var called = false
        Thread.setDefaultUncaughtExceptionHandler { _, _ -> called = true }
        val blocked = File(dir, "file").apply { writeText("") }
        val sdk = OpenLog.start(options().copy(crashDirectory = File(blocked, "below").path), send = { _, _, _ -> 200 }, fetchConfig = { _, _ -> null })
        die()
        assertTrue(called)
        sdk.shutdown()
    }

    @Test
    fun `the next launch delivers it with the crashed session and removes it`() {
        var crashedSession = ""
        run {
            val sdk = start()
            crashedSession = sdk.sessionId
            die()
            OpenLog.reset()
        }
        val delivered = CountDownLatch(1)
        var body = ""
        var headers = emptyMap<String, String>()
        val sdk = start(send = { _, h, b ->
            if (b.contains("uncaught_exception")) {
                body = b
                headers = h
                delivered.countDown()
            }
            200
        })
        assertTrue(delivered.await(5, TimeUnit.SECONDS))
        assertTrue(body.contains(crashedSession), "not the session of this launch: $body")
        assertTrue(sdk.sessionId != crashedSession)
        assertEquals("com.example", headers["openlog-app-id"])
        for (i in 1..100) {
            if (CrashStore(dir.path).pending().isEmpty()) break
            Thread.sleep(10)
        }
        assertTrue(CrashStore(dir.path).pending().isEmpty())
        sdk.shutdown()
    }

    @Test
    fun `keeps a report until the server has answered for good`() {
        val store = CrashStore(dir.path)
        store.write("a", "{}")
        for (status in listOf(-1, 429, 503)) {
            deliverCrashes(store) { status }
            assertEquals(1, store.pending().size, "after $status")
        }
        deliverCrashes(store) { 200 }
        assertTrue(store.pending().isEmpty())
        store.write("b", "{}")
        deliverCrashes(store) { 400 }
        assertTrue(store.pending().isEmpty(), "a body the server refuses will not improve")
    }

    @Test
    fun `sends reports oldest first and stops at the first it cannot send`() {
        var now = 1000L
        val store = CrashStore(dir.path) { now++ }
        store.write("a", "first")
        store.write("b", "second")
        val seen = ArrayList<String>()
        deliverCrashes(store) { seen.add(it); -1 }
        assertEquals(listOf("first"), seen)
    }

    @Test
    fun `a crash loop cannot fill the device, and keeps its first reports`() {
        var now = 1000L
        val store = CrashStore(dir.path) { now++ }
        for (i in 1..(MAX_CRASH_FILES + 5)) store.write("id$i", "report $i")
        val files = store.pending()
        assertEquals(MAX_CRASH_FILES, files.size)
        assertEquals("report 1", files.first().readText())
    }

    @Test
    fun `a build says it can report crashes only when it can`() {
        var body = ""
        val sdk = start(send = { _, _, b -> body = b; 200 })
        sdk.recordScreen("/x")
        sdk.flush()
        assertTrue(body.contains("openlog.rum.crash.capture"), body)
        sdk.shutdown()

        var plain = ""
        val other = OpenLog.start(options(capture = false), send = { _, _, b -> plain = b; 200 }, fetchConfig = { _, _ -> null })
        other.recordScreen("/x")
        other.flush()
        assertNotNull(plain)
        assertFalse(plain.contains("openlog.rum.crash.capture"), plain)
        other.shutdown()
    }
}
