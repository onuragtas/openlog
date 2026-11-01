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

    class FakeExit(
        private val reason: Int,
        private val at: Long,
        private val trace: ByteArray? = null,
        private val importance: Int = 100,
    ) {
        fun getReason() = reason
        fun getTimestamp() = at
        fun getDescription(): String? = "from the system"
        fun getImportance() = importance
        fun getTraceInputStream(): java.io.InputStream? = trace?.inputStream()
    }

    class FakeManager(private val exits: List<FakeExit>) {
        fun getHistoricalProcessExitReasons(pkg: String, pid: Int, max: Int): List<FakeExit> = exits
    }

    class FakeContext(exits: List<FakeExit>) {
        private val manager = FakeManager(exits)
        fun getPackageName() = "com.example"
        fun getSystemService(name: String): Any? = if (name == "activity") manager else null
    }

    private fun exits(vararg e: FakeExit) = readAndroidExits(FakeContext(e.toList()), dir.path, sdkInt = { 31 })

    private fun varint(v: Long): ByteArray {
        val out = java.io.ByteArrayOutputStream()
        var n = v
        while (n >= 0x80) {
            out.write(((n and 0x7f) or 0x80).toInt())
            n = n ushr 7
        }
        out.write(n.toInt())
        return out.toByteArray()
    }

    private fun int(field: Int, v: Long) = varint((field shl 3).toLong()) + varint(v)
    private fun bytes(field: Int, b: ByteArray) = varint(((field shl 3) or 2).toLong()) + varint(b.size.toLong()) + b
    private fun text(field: Int, s: String) = bytes(field, s.toByteArray())

    private fun tombstone(): ByteArray {
        val signal = int(1, 11) + text(2, "SIGSEGV") + text(4, "SEGV_MAPERR")
        val frame = int(1, 0x1a2b) + int(2, 0x7000001a2b) + text(4, "") + text(6, "/data/app/~~x==/lib/arm64/libgame.so") +
            text(8, "0123456789abcdef0123456789abcdef")
        val named = int(1, 0x50) + text(4, "abort") + text(6, "/apex/bionic/libc.so") + text(8, "fedcba9876543210")
        val crashed = int(1, 42) + bytes(4, frame) + bytes(4, named)
        val other = int(1, 7) + bytes(4, int(1, 1) + text(6, "/x/other.so"))
        return int(6, 42) + bytes(10, signal) + bytes(16, int(1, 7) + bytes(2, other)) + bytes(16, int(1, 42) + bytes(2, crashed))
    }

    @Test
    fun aTombstoneIsReadFromTheCrashedThread() {
        val t = assertNotNull(decodeTombstone(tombstone()))
        assertEquals("SIGSEGV (SEGV_MAPERR)", t.message())
        assertEquals(
            "#00 libgame.so 0123456789abcdef0123456789abcdef +0x1a2b\n#01 libc.so fedcba9876543210 +0x50 abort\n",
            t.stack,
        )
    }

    @Test
    fun whatIsNotATombstoneIsRefused() {
        assertEquals(null, decodeTombstone("*** a text trace".toByteArray()))
        assertEquals(null, decodeTombstone(byteArrayOf(0x82.toByte(), 1, 0xff.toByte(), 0xff.toByte(), 0x7f)))
        assertEquals(null, decodeTombstone(ByteArray(0)))
    }

    @Test
    fun exitsFromBeforeTheSdkAreNotReported() {
        assertTrue(exits(FakeExit(5, 1000)).isEmpty())
        val later = exits(FakeExit(5, 1000), FakeExit(5, 2000, tombstone()))
        assertEquals(listOf("SIGSEGV"), later.map { it.type })
        assertEquals(2000L, later[0].atMs)
        assertTrue(later[0].stack.startsWith("#00 libgame.so"))
        assertTrue(exits(FakeExit(5, 2000, tombstone())).isEmpty(), "reported twice")
    }

    @Test
    fun eachKindOfExitIsReportedForWhatItIs() {
        exits(FakeExit(5, 1000))
        val got = exits(
            FakeExit(5, 2000), // API 30 keeps no tombstone
            FakeExit(6, 2001, "\"main\" prio=5\n  at a.b.c(SourceFile:3)".toByteArray()),
            FakeExit(3, 2002),
            FakeExit(3, 2003, importance = 400), // the system reclaiming a background application
            FakeExit(4, 2004), // a Java crash: the handler in the process already said so
            FakeExit(1, 2005), // the application closing itself
        )
        assertEquals(listOf("NativeCrash", "ANR", "OutOfMemory"), got.map { it.type })
        assertEquals("from the system", got[0].message)
        assertTrue(got[1].stack.contains("at a.b.c"))
    }

    private fun startWithExit(sdk: Int, onBody: (String) -> Unit) = OpenLog.start(
        options().copy(androidContext = FakeContext(listOf(FakeExit(5, 2000, tombstone())))),
        send = { url, _, body ->
            if (url.contains("/v1/rum")) onBody(body)
            200
        },
        fetchConfig = { _, _ -> null },
        androidSdk = { sdk },
    )

    @Test
    fun anAndroidExitIsDeliveredWithTheBuildAndSessionItHappenedIn() {
        LastSession("00000000000000000000000000000abc", "1.2.2").write(dir.path)
        File(dir, "openlog-last-exit").writeText("1000")
        val latch = CountDownLatch(1)
        var sent = ""
        startWithExit(31) {
            sent = it
            latch.countDown()
        }
        assertTrue(latch.await(5, TimeUnit.SECONDS))
        for (want in listOf("SIGSEGV", "00000000000000000000000000000abc", "1.2.2", "libgame.so 0123456789abcdef0123456789abcdef +0x1a2b")) {
            assertTrue(sent.contains(want), "$want not in $sent")
        }
    }

    @Test
    fun withNoAndroidUnderneathAContextDoesNothing() {
        File(dir, "openlog-last-exit").writeText("1000")
        val latch = CountDownLatch(1)
        startWithExit(0) { latch.countDown() }
        assertFalse(latch.await(300, TimeUnit.MILLISECONDS))
    }

    @Test
    fun anAnrStartsAtTheThreadThatDidNotRespond() {
        assertEquals("\"main\" prio=5\n at a.b.c(SourceFile:3)", anrStack("Subject: x\nRssKb: 1\n\n\"main\" prio=5\n at a.b.c(SourceFile:3)"))
        assertEquals("no threads", anrStack("no threads"))
    }

    @Test
    fun aStackIsNotCutToTheLimitOfAnAttribute() {
        val span = buildSpan("e", "error", "t", "s", 1, isError = true, exception = Triple("T", "m", "x".repeat(9000)))
        assertTrue(jsonEncode(span).contains("x".repeat(9000)))
    }

    @Test
    fun aFrameIsWrittenTheWayTheServerReadsIt() {
        assertEquals("#03 My_Game.so abc +0x10 f", nativeFrame(3, "/a/b/My Game.so", "abc", 16, "f"))
        assertEquals("#00 unknown +0x0", nativeFrame(0, "", "", 0, ""))
    }
}
