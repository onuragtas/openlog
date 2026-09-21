package io.github.onuragtas.openlog

// Options and their validation (docs/contracts/mobile-agent.md §1, §5).
//
// Everything that bounds an application — which applications a key serves, how much it may send, what share
// of sessions it keeps — lives on the key, not here. What is left is where to send and what this build
// calls itself, which only the application knows.

/** Thrown when the SDK is given options it cannot work with. Thrown rather than logged: an SDK that
 *  silently does nothing is worse than one that refuses to start, because nobody finds out for weeks. */
class ConfigException(message: String) : IllegalArgumentException("openlog: $message")

data class Options(
    /** The mobile key (`olb_…`). Public by construction: it ships inside the application. */
    val key: String,
    /** OTLP/HTTP base URL of openlog ingest, e.g. `https://ingest.example.com:4318`. */
    val endpoint: String,
    /** The Android package name, sent as `openlog-app-id`. Required: a mobile key is scoped by an
     *  application allowlist, and a request that declares nothing never matches one. */
    val appId: String,
    /** The build. `service.name` comes from the key and this does not — the name decides whose data this
     *  is, the version only labels a build within it. */
    val serviceVersion: String = "",
    val deviceModel: String = "",
    val deviceManufacturer: String = "",
    val osName: String = "Android",
    val osVersion: String = "",
    val sampleRate: Double = 1.0,
    val maxBatchSize: Int = 32,
    val flushIntervalMs: Long = 5_000,
    val debug: Boolean = false,
)

internal class ResolvedConfig(val options: Options, val rumUrl: String, val configUrl: String) {
    /** Set by `GET /v1/rum/config`; until it answers these are the compiled-in ones. */
    var serviceName: String = ""
    var environment: String = ""
}

internal fun resolveConfig(o: Options): ResolvedConfig {
    if (!o.key.trim().startsWith("olb_")) throw ConfigException("`key` must be a browser or mobile key (olb_…)")
    val endpoint = o.endpoint.trim().trimEnd('/')
    if (!endpoint.startsWith("http://") && !endpoint.startsWith("https://")) {
        throw ConfigException("`endpoint` must be the http(s) URL of openlog ingest")
    }
    if (o.appId.isBlank()) {
        throw ConfigException("`appId` is required: a mobile key is scoped by the applications it ships in")
    }
    if (o.sampleRate <= 0 || o.sampleRate > 1) throw ConfigException("`sampleRate` must be greater than 0 and at most 1")
    if (o.maxBatchSize < 1 || o.maxBatchSize > 1000) throw ConfigException("`maxBatchSize` must be between 1 and 1000")
    if (o.flushIntervalMs < 500 || o.flushIntervalMs > 60_000) {
        throw ConfigException("`flushIntervalMs` must be between 500 and 60000")
    }
    return ResolvedConfig(o, "$endpoint/v1/rum", "$endpoint/v1/rum/config")
}
