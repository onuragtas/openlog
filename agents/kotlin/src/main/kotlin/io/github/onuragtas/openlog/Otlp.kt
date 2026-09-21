package io.github.onuragtas.openlog

// OTLP/JSON encoding (docs/contracts/mobile-agent.md §3).

internal object RumEvent {
    const val SCREEN = "page_view"
    const val VITAL = "vital"
    const val ERROR = "error"
    const val RESOURCE = "resource"
    const val CUSTOM = "custom"
}

/** Nanoseconds as a string: milliseconds are integers here, and concatenation avoids the rounding that
 *  multiplying by 1e6 introduces past 2^53. */
internal fun nanoString(ms: Long): String = "${if (ms < 0) 0 else ms}000000"

/** Every attribute travels as a string: the backend stores span attributes as Map(String, String).
 *  Ordered pairs rather than a map so the cap removes the last ones rather than an arbitrary set. */
internal fun encodeAttributes(attrs: List<Pair<String, Any?>>): List<Map<String, Any>> {
    val out = ArrayList<Map<String, Any>>()
    for ((key, value) in attrs) {
        if (value == null) continue
        val s = value as? String ?: value.toString()
        if (s.isEmpty()) continue // an empty attribute is not a value, and the server drops it anyway
        if (out.size >= Limits.MAX_ATTRIBUTES_PER_SPAN) break
        val limit = if (key == "url.full") Limits.MAX_URL_BYTES else Limits.MAX_ATTR_VALUE_BYTES
        out.add(mapOf("key" to key, "value" to mapOf("stringValue" to truncate(s, limit))))
    }
    return out
}

internal fun buildSpan(
    name: String,
    event: String,
    traceId: String,
    spanId: String,
    startMs: Long,
    durationMs: Long = 0,
    parentSpanId: String? = null,
    attributes: List<Pair<String, Any?>> = emptyList(),
    isError: Boolean = false,
    exception: Triple<String, String, String>? = null,
): Map<String, Any?> {
    val span = LinkedHashMap<String, Any?>()
    span["traceId"] = traceId
    span["spanId"] = spanId
    if (parentSpanId != null) span["parentSpanId"] = parentSpanId
    span["name"] = truncate(name, Limits.MAX_NAME_BYTES)
    // INTERNAL for screens, vitals and errors; CLIENT for requests, like the other clients.
    span["kind"] = if (event == RumEvent.RESOURCE) 3 else 1
    span["startTimeUnixNano"] = nanoString(startMs)
    span["endTimeUnixNano"] = nanoString(startMs + if (durationMs < 0) 0 else durationMs)
    span["attributes"] = encodeAttributes(listOf("openlog.rum.event" to event) + attributes)
    if (exception != null) {
        val (type, message, stack) = exception
        span["events"] = listOf(
            mapOf(
                "timeUnixNano" to nanoString(startMs),
                "name" to "exception",
                "attributes" to encodeAttributes(
                    listOf(
                        "exception.type" to type,
                        "exception.message" to truncate(message, Limits.MAX_MESSAGE_BYTES),
                        "exception.stacktrace" to truncate(stack, Limits.MAX_STACK_BYTES),
                    )
                ),
            )
        )
    }
    if (isError) span["status"] = mapOf("code" to 2)
    return span
}

/** The request body: one resource with its spans. */
internal fun buildPayload(spans: List<Map<String, Any?>>, resource: List<Pair<String, Any?>>): Map<String, Any?> =
    mapOf(
        "resourceSpans" to listOf(
            mapOf(
                "resource" to mapOf("attributes" to encodeAttributes(resource)),
                "scopeSpans" to listOf(mapOf("spans" to spans)),
            )
        )
    )
