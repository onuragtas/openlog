// Options and their validation (docs/contracts/mobile-agent.md §1, §5).
//
// Everything that bounds an application — which applications a key serves, how much it may send, what share
// of sessions it keeps — lives on the key, not here. What is left is where to send and what this build
// calls itself, which only the application knows.

import Foundation

/// Thrown when the SDK is given options it cannot work with. Thrown rather than logged: an SDK that
/// silently does nothing is worse than one that refuses to start, because nobody finds out for weeks.
public struct ConfigError: Error, CustomStringConvertible {
    public let message: String
    public var description: String { "openlog: \(message)" }
}

/// What an application passes to `OpenLog.start`.
public struct Options {
    /// The mobile key (`olb_…`). Public by construction: it ships inside the application binary, and the
    /// server bounds what it can do.
    public var key: String
    /// OTLP/HTTP base URL of openlog ingest, e.g. `https://ingest.example.com:4318`.
    public var endpoint: String
    /// The bundle identifier, sent as `openlog-app-id`. Required: a mobile key is scoped by an application
    /// allowlist, and a request that declares nothing never matches one.
    public var appID: String

    /// The build. `service.name` comes from the key and this does not — the name decides whose data this
    /// is, the version only labels a build within it.
    public var serviceVersion: String
    public var deviceModel: String
    public var deviceManufacturer: String
    public var osName: String
    public var osVersion: String

    public var sampleRate: Double
    public var maxBatchSize: Int
    public var flushInterval: TimeInterval
    public var debug: Bool

    public init(
        key: String,
        endpoint: String,
        appID: String,
        serviceVersion: String = "",
        deviceModel: String = "",
        deviceManufacturer: String = "Apple",
        osName: String = "",
        osVersion: String = "",
        sampleRate: Double = 1,
        maxBatchSize: Int = 32,
        flushInterval: TimeInterval = 5,
        debug: Bool = false
    ) {
        self.key = key
        self.endpoint = endpoint
        self.appID = appID
        self.serviceVersion = serviceVersion
        self.deviceModel = deviceModel
        self.deviceManufacturer = deviceManufacturer
        self.osName = osName
        self.osVersion = osVersion
        self.sampleRate = sampleRate
        self.maxBatchSize = maxBatchSize
        self.flushInterval = flushInterval
        self.debug = debug
    }
}

/// Options after validation, with the URLs derived once.
final class ResolvedConfig {
    let options: Options
    let rumURL: String
    let configURL: String

    /// Set by `GET /v1/rum/config`; until it answers these are the compiled-in ones.
    var serviceName = ""
    var environment = ""

    init(options: Options, rumURL: String, configURL: String) {
        self.options = options
        self.rumURL = rumURL
        self.configURL = configURL
    }
}

/// Validates and derives. The checks are the server's own bounds, applied here so a mistake surfaces at
/// start-up in development rather than as a 400 in production.
func resolveConfig(_ o: Options) throws -> ResolvedConfig {
    guard o.key.trimmingCharacters(in: .whitespaces).hasPrefix("olb_") else {
        throw ConfigError(message: "`key` must be a browser or mobile key (olb_…)")
    }
    var endpoint = o.endpoint.trimmingCharacters(in: .whitespaces)
    while endpoint.hasSuffix("/") { endpoint.removeLast() }
    guard endpoint.hasPrefix("http://") || endpoint.hasPrefix("https://") else {
        throw ConfigError(message: "`endpoint` must be the http(s) URL of openlog ingest")
    }
    guard !o.appID.trimmingCharacters(in: .whitespaces).isEmpty else {
        throw ConfigError(message: "`appID` is required: a mobile key is scoped by the applications it ships in")
    }
    guard o.sampleRate > 0, o.sampleRate <= 1 else {
        throw ConfigError(message: "`sampleRate` must be greater than 0 and at most 1")
    }
    guard o.maxBatchSize >= 1, o.maxBatchSize <= 1000 else {
        throw ConfigError(message: "`maxBatchSize` must be between 1 and 1000")
    }
    guard o.flushInterval >= 0.5, o.flushInterval <= 60 else {
        throw ConfigError(message: "`flushInterval` must be between 500ms and 60s")
    }
    return ResolvedConfig(options: o, rumURL: "\(endpoint)/v1/rum", configURL: "\(endpoint)/v1/rum/config")
}
