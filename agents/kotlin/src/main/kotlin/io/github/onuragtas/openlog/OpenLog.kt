package io.github.onuragtas.openlog

import java.net.HttpURLConnection
import java.net.URL

// openlog mobile SDK for the JVM (docs/contracts/mobile-agent.md).
//
// What this does NOT do is as deliberate as what it does: it does not see an NDK crash or an ANR. Those are not JVM
// exceptions, and reading them needs an Android API this plain JVM library does not have (mobile-crash.md §2).

/** Reads `GET /v1/rum/config`, or null when it could not be read. */
typealias ConfigFetch = (url: String, headers: Map<String, String>) -> Map<String, Any?>?

/**
 * Pulls the fields the config endpoint returns out of its body.
 *
 * **Not a JSON parser, and not pretending to be one.** This SDK has no dependencies and the response is
 * four known scalars; a hand-written general parser would be more code and more ways to be subtly wrong
 * than the problem deserves. Anything it cannot find is simply absent, which is the same as the call
 * having failed — and failure here is already silent by design.
 */
internal fun parseConfigFields(body: String): Map<String, Any?> {
    val out = LinkedHashMap<String, Any?>()
    Regex(""""service_name"\s*:\s*"([^"]*)"""").find(body)?.let { out["service_name"] = it.groupValues[1] }
    Regex(""""environment"\s*:\s*"([^"]*)"""").find(body)?.let { out["environment"] = it.groupValues[1] }
    Regex(""""analytics_mode"\s*:\s*"([^"]*)"""").find(body)?.let { out["analytics_mode"] = it.groupValues[1] }
    Regex(""""sample_rate"\s*:\s*([0-9.eE+-]+)""").find(body)?.let {
        it.groupValues[1].toDoubleOrNull()?.let { rate -> out["sample_rate"] = rate }
    }
    return out
}

internal fun defaultFetchConfig(url: String, headers: Map<String, String>): Map<String, Any?>? = try {
    val conn = URL(url).openConnection() as HttpURLConnection
    try {
        conn.connectTimeout = 10_000
        conn.readTimeout = 10_000
        headers.forEach { (k, v) -> conn.setRequestProperty(k, v) }
        if (conn.responseCode != 200) null
        else parseConfigFields(conn.inputStream.bufferedReader().use { it.readText() })
    } finally {
        conn.disconnect()
    }
} catch (_: Exception) {
    null // the SDK reports with its compiled-in settings; nothing depends on this call to start
}

/** The SDK. One per application: a second would double-count every screen. */
class OpenLog private constructor(
    private val session: SessionState,
    private val transport: Transport,
    private val visitor: Visitor,
) {
    @Volatile
    private var userId: String = ""

    /** The screen last opened, which is where a crash happened. */
    @Volatile
    private var route: String = ""

    private var crashes: CrashStore? = null
    private var crashHandler: Thread.UncaughtExceptionHandler? = null
    private var previousHandler: Thread.UncaughtExceptionHandler? = null

    companion object {
        private val lock = Any()

        @Volatile
        private var active: OpenLog? = null

        /**
         * Starts the SDK. Calling it twice returns the first instance rather than starting a second.
         *
         * [send] and [fetchConfig] exist so tests never open a socket; applications leave them unset.
         */
        @JvmStatic
        @JvmOverloads
        fun start(
            options: Options,
            send: HttpSend = ::defaultSend,
            fetchConfig: ConfigFetch = ::defaultFetchConfig,
        ): OpenLog {
            active?.let { return it }
            synchronized(lock) {
                active?.let { return it }
                val cfg = resolveConfig(options) // throws rather than starting a silently useless SDK
                val visitor = Visitor(options.analyticsCollection, options.visitorDirectory)
                val session = SessionState(options.sampleRate, roll = {
                    visitor.id.let { id -> if (id.isEmpty()) nextUnit() else visitorRoll(id) }
                })
                val sdk = OpenLog(session, Transport(cfg, send), visitor)
                active = sdk
                if (options.captureCrashes && options.crashDirectory != null) {
                    sdk.startCrashCapture(CrashStore(options.crashDirectory))
                }

                // The operator's sample rate wins, so volume can be turned down without shipping a
                // release. Until it answers the application reports with its own; failure is silent.
                Thread({
                    // A mobile key refuses a request that names no application, and this one is no exception.
                    val conf = fetchConfig(
                        cfg.configUrl,
                        mapOf("openlog-browser-key" to options.key, "openlog-app-id" to options.appId),
                    )
                    if (conf != null) {
                        cfg.serviceName = conf["service_name"] as? String ?: ""
                        cfg.environment = conf["environment"] as? String ?: ""
                        (conf["sample_rate"] as? Double)?.let { sdk.session.sampleRate = it }
                        // Only this answer lets the id reach the disk. A server without the field is one
                        // without the feature.
                        if (conf["analytics_mode"] == "visitors") visitor.confirm() else visitor.discard()
                    }
                }, "openlog-config").apply { isDaemon = true }.start()
                return sdk
            }
        }

        /** Drops the active instance (tests, and shutdown()). */
        @JvmStatic
        fun reset() {
            synchronized(lock) { active = null }
        }
    }

    /** Delivers what the last launch left, and installs the handler in front of whichever was already there. */
    private fun startCrashCapture(store: CrashStore) {
        crashes = store
        // Listed before the handler exists, so this launch's own crash cannot be sent by a thread it is killing.
        val waiting = store.pending()
        if (waiting.isNotEmpty()) {
            Thread({ deliverCrashes(store) { transport.sendBody(it) } }, "openlog-crashes").apply { isDaemon = true }.start()
        }
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        val handler = Thread.UncaughtExceptionHandler { thread, error ->
            try {
                recordCrash(error)
            } catch (_: Throwable) {
                // Whatever went wrong here, the application's own handling of its crash still has to run.
            }
            if (previous != null) previous.uncaughtException(thread, error) else error.printStackTrace()
        }
        previousHandler = previous
        crashHandler = handler
        Thread.setDefaultUncaughtExceptionHandler(handler)
    }

    /** One write and nothing else: the process is going down (mobile-crash.md §2.1). */
    private fun recordCrash(error: Throwable) {
        val store = crashes ?: return
        if (!session.shouldSend) return // a sampled-out session sends nothing at all, crash included
        val id = newSessionId()
        val type = error.javaClass.name
        val span = buildSpan(
            name = "error $type",
            event = RumEvent.ERROR,
            traceId = session.traceId,
            spanId = newSpanId(),
            startMs = System.currentTimeMillis(),
            parentSpanId = session.screenSpanId,
            attributes = commonAttributes() + listOf(
                "openlog.rum.route" to route,
                "openlog.rum.error.source" to "error",
                "openlog.rum.crash.kind" to "uncaught_exception",
                "openlog.rum.crash.id" to id,
            ),
            isError = true,
            exception = Triple(type, error.message ?: type, error.stackTraceToString()),
        )
        store.write(id, transport.encode(listOf(span)))
    }

    /** The current session id, or "" when this session was not sampled. */
    val sessionId: String get() = if (session.shouldSend) session.id else ""

    /**
     * Attaches the application's own identifier for the person to every later span. Pass "" on sign-out.
     *
     * **Send an opaque, stable id — not an e-mail address or a name.** openlog stores it and never
     * interprets it, so the discipline has to live here (§3.1).
     */
    fun identify(id: String) {
        userId = truncate(id.trim(), Limits.MAX_USER_ID_BYTES)
    }

    /**
     * Turns recognising this person across launches on or off, e.g. from a consent dialog (analytics.md §1). Off deletes
     * the stored id and is remembered across launches. It stops collection and erases nothing already sent: that is
     * `POST /api/v1/analytics/erase`, called from your backend with [visitorId].
     */
    fun setAnalyticsCollection(enabled: Boolean) = visitor.setEnabled(enabled)

    /** The current visitor id, or "" when there is none. */
    val visitorId: String get() = visitor.id

    /** A screen the person opened. [isColdStart] marks the first one of a launch. */
    @JvmOverloads
    fun recordScreen(name: String, isColdStart: Boolean = false, durationMs: Long = 0) {
        val route = truncate(name.trim(), Limits.MAX_NAME_BYTES)
        if (route.isEmpty()) return
        this.route = route
        if (!session.touch()) session.newScreen()
        emit(
            RumEvent.SCREEN, "screen $route", durationMs = durationMs, spanId = session.screenSpanId,
            attributes = listOf(
                "openlog.rum.route" to route,
                "openlog.rum.page_view.kind" to if (isColdStart) "load" else "route_change",
            ),
        )
    }

    /** An error the application caught itself. */
    fun recordError(error: Throwable) {
        val type = error.javaClass.simpleName
        emit(
            RumEvent.ERROR, "error $type", isError = true,
            attributes = listOf("openlog.rum.error.source" to "error"),
            exception = Triple(type, error.message ?: type, error.stackTraceToString()),
        )
    }

    /** An application-defined event, e.g. `recordEvent("checkout_started", mapOf("plan" to "pro"))`. */
    @JvmOverloads
    fun recordEvent(name: String, params: Map<String, Any?> = emptyMap()) {
        val n = truncate(name.trim(), Limits.MAX_CUSTOM_NAME_BYTES)
        if (n.isEmpty()) return // an unnamed event is an unqueryable row, and the server refuses it anyway
        emit(RumEvent.CUSTOM, n, attributes = listOf("openlog.rum.custom.name" to n) + customParams(params))
    }

    /** An application-defined timing in milliseconds. */
    @JvmOverloads
    fun recordTiming(name: String, milliseconds: Long, params: Map<String, Any?> = emptyMap()) {
        val n = truncate(name.trim(), Limits.MAX_CUSTOM_NAME_BYTES)
        if (n.isEmpty()) return
        emit(
            RumEvent.CUSTOM, n, durationMs = milliseconds,
            attributes = listOf(
                "openlog.rum.custom.name" to n,
                "openlog.rum.custom.value" to milliseconds,
                "openlog.rum.custom.unit" to "ms",
            ) + customParams(params),
        )
    }

    /** Parameters live in a namespace of their own so they can never collide with a field openlog later
     *  learns to interpret. Sorted before the cap is applied: which sixteen survive must depend on the
     *  payload, not on map order. */
    private fun customParams(params: Map<String, Any?>): List<Pair<String, Any?>> {
        val out = ArrayList<Pair<String, Any?>>()
        for (key in params.keys.sorted()) {
            if (out.size >= Limits.MAX_CUSTOM_PARAMS) break
            val k = key.trim()
            if (k.isEmpty() || k.length > Limits.MAX_CUSTOM_PARAM_KEY_BYTES) continue
            if (!Regex("^[a-z0-9_.-]+$").matches(k)) continue
            out.add("openlog.rum.custom.param.$k" to params[key])
        }
        return out
    }

    private fun commonAttributes(): List<Pair<String, Any?>> {
        val base = ArrayList<Pair<String, Any?>>()
        base.add("session.id" to session.id)
        base.add("openlog.rum.page_view.id" to session.screenSpanId)
        val user = userId
        if (user.isNotEmpty()) base.add("user.id" to user)
        val visitorId = visitor.id
        if (visitorId.isNotEmpty()) base.add("openlog.rum.visitor.id" to visitorId)
        return base
    }

    private fun emit(
        event: String,
        name: String,
        durationMs: Long = 0,
        spanId: String? = null,
        attributes: List<Pair<String, Any?>> = emptyList(),
        isError: Boolean = false,
        exception: Triple<String, String, String>? = null,
    ) {
        if (!session.shouldSend) return // a sampled-out session sends nothing at all
        session.touch()
        val base = commonAttributes()

        transport.add(
            buildSpan(
                name = name,
                event = event,
                traceId = session.traceId,
                spanId = spanId ?: newSpanId(),
                startMs = System.currentTimeMillis(),
                durationMs = durationMs,
                // Everything hangs off the screen, so one trace holds the whole of it.
                parentSpanId = if (spanId == null) session.screenSpanId else null,
                attributes = base + attributes,
                isError = isError,
                exception = exception,
            )
        )
    }

    /** Sends what is queued. Call this when the application goes to the background: on a mobile platform
     *  it is the last reliable moment, and the batch most likely to be lost is the final one. */
    fun onAppBackgrounded() = transport.flush()

    fun flush() = transport.flush()

    fun shutdown() {
        // Only if it is still ours: something installed after this has our handler behind it, and must keep it.
        if (crashHandler != null && Thread.getDefaultUncaughtExceptionHandler() === crashHandler) {
            Thread.setDefaultUncaughtExceptionHandler(previousHandler)
        }
        transport.stop()
        reset()
    }
}
