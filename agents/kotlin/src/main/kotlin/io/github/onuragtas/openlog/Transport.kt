package io.github.onuragtas.openlog

import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit

// Batching and delivery (docs/contracts/mobile-agent.md §5, §6).
//
// Sending is injectable so the tests never open a socket: what is worth testing here is when a batch goes
// out and what happens to it when the server says no, neither of which needs a network to be wrong.

/** Posts a body and answers with the HTTP status, or -1 when the request never completed. */
internal typealias HttpSend = (url: String, headers: Map<String, String>, body: String) -> Int

/** The default sender. HttpURLConnection rather than OkHttp: this SDK has no dependencies, and an
 *  application must never have to resolve a version of one because its telemetry library wanted it. */
internal fun defaultSend(url: String, headers: Map<String, String>, body: String): Int = try {
    val conn = URL(url).openConnection() as HttpURLConnection
    try {
        conn.requestMethod = "POST"
        conn.doOutput = true
        conn.connectTimeout = 10_000
        conn.readTimeout = 10_000
        headers.forEach { (k, v) -> conn.setRequestProperty(k, v) }
        conn.outputStream.use { it.write(body.toByteArray(Charsets.UTF_8)) }
        conn.responseCode
    } finally {
        conn.disconnect()
    }
} catch (_: Exception) {
    -1
}

internal class Transport(
    private val cfg: ResolvedConfig,
    private val send: HttpSend = ::defaultSend,
) {
    private val lock = Any()
    private val queue = ArrayList<Map<String, Any?>>()
    private var scheduler: ScheduledExecutorService? = null
    private var pending: ScheduledFuture<*>? = null

    /** Set when the server refused the key itself. Nothing about the next attempt would differ, so the SDK
     *  stops rather than spending the device's battery on a permanent answer (§6). */
    @Volatile
    var refused: Boolean = false
        private set

    fun add(span: Map<String, Any?>) {
        val full: Boolean
        synchronized(lock) {
            if (refused) return
            if (queue.size >= Limits.MAX_SPANS_PER_REQUEST) {
                // Drop the oldest: the newest events are the ones still worth having, and an unbounded
                // queue in a long-lived application is a leak the application would be blamed for.
                queue.removeAt(0)
            }
            queue.add(span)
            full = queue.size >= cfg.options.maxBatchSize
        }
        if (full) flush() else schedule()
    }

    private fun schedule() {
        synchronized(lock) {
            if (pending != null) return
            val exec = scheduler ?: Executors.newSingleThreadScheduledExecutor { r ->
                Thread(r, "openlog-flush").apply { isDaemon = true } // never hold the application open
            }.also { scheduler = it }
            pending = exec.schedule({
                synchronized(lock) { pending = null }
                flush()
            }, cfg.options.flushIntervalMs, TimeUnit.MILLISECONDS)
        }
    }

    fun flush() {
        val batch: List<Map<String, Any?>>
        synchronized(lock) {
            pending?.cancel(false)
            pending = null
            if (refused || queue.isEmpty()) return
            batch = ArrayList(queue)
            queue.clear()
        }
        val body = jsonEncode(buildPayload(batch, resource()))
        val status = send(
            cfg.rumUrl,
            mapOf(
                "content-type" to "application/json",
                "openlog-browser-key" to cfg.options.key,
                "openlog-app-id" to cfg.options.appId,
            ),
            body,
        )
        finish(status, batch)
    }

    private fun finish(status: Int, batch: List<Map<String, Any?>>) {
        if (status == 401 || status == 403) {
            refused = true
            log("the server refused this key ($status); nothing further will be sent")
            return
        }
        // 429 and 503 are temporary and say so; the batch goes back so the next flush carries it. Anything
        // else — including a request that never completed — is dropped rather than retried forever.
        if (status != 429 && status != 503 && status != -1) return
        synchronized(lock) {
            val room = (Limits.MAX_SPANS_PER_REQUEST - queue.size).coerceAtLeast(0)
            queue.addAll(0, batch.take(room))
        }
        schedule()
    }

    private fun resource(): List<Pair<String, Any?>> = listOf(
        "service.version" to cfg.options.serviceVersion,
        "device.model.identifier" to cfg.options.deviceModel,
        "device.manufacturer" to cfg.options.deviceManufacturer,
        "os.name" to cfg.options.osName,
        "os.version" to cfg.options.osVersion,
    )

    private fun log(message: String) {
        if (cfg.options.debug) System.err.println("[openlog] $message")
    }

    /** Sends what is queued and stops the timer. Called when the application goes to the background, which
     *  is the last reliable moment on a mobile platform. */
    fun stop() {
        flush()
        synchronized(lock) {
            pending?.cancel(false)
            pending = null
            scheduler?.shutdownNow()
            scheduler = null
        }
    }
}
