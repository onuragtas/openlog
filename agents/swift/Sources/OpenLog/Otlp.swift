// OTLP/JSON encoding (docs/contracts/mobile-agent.md §3).
//
// Hand-written rather than generated from the protobuf: the payload is four nested objects and a list of
// attributes, and a code generator plus its runtime would be the dependency this package exists without.

import Foundation

/// The event kinds the server accepts. A span without one is not RUM and is dropped.
enum RumEvent {
    static let screen = "page_view"
    static let vital = "vital"
    static let error = "error"
    static let resource = "resource"
    static let custom = "custom"
}

/// Nanoseconds as a string: milliseconds are integers here, and concatenation avoids the rounding that
/// multiplying by 1e6 introduces past 2^53.
func nanoString(_ ms: Int64) -> String { "\(max(0, ms))000000" }

/// Every attribute travels as a string: the backend stores span attributes as Map(String, String).
func encodeAttributes(_ attrs: [(String, Any?)]) -> [[String: Any]] {
    var out: [[String: Any]] = []
    for (key, value) in attrs {
        guard let value else { continue }
        let s: String
        switch value {
        case let v as String: s = v
        case let v as Bool: s = v ? "true" : "false"
        default: s = "\(value)"
        }
        if s.isEmpty { continue }  // an empty attribute is not a value, and the server drops it anyway
        if out.count >= Limits.maxAttributesPerSpan { break }
        let limit = key == "url.full" ? Limits.maxURLBytes : Limits.maxAttrValueBytes
        out.append(["key": key, "value": ["stringValue": truncate(s, limit)]])
    }
    return out
}

/// One span of a session. Attributes are ordered pairs rather than a dictionary so the cap above removes
/// the last ones rather than an arbitrary set.
func buildSpan(
    name: String,
    event: String,
    traceID: String,
    spanID: String,
    startMs: Int64,
    durationMs: Int64 = 0,
    parentSpanID: String? = nil,
    attributes: [(String, Any?)] = [],
    isError: Bool = false,
    exception: (type: String, message: String, stacktrace: String)? = nil
) -> [String: Any] {
    var span: [String: Any] = [
        "traceId": traceID,
        "spanId": spanID,
        "name": truncate(name, Limits.maxNameBytes),
        // INTERNAL for screens, vitals and errors; CLIENT for requests, like the other clients.
        "kind": event == RumEvent.resource ? 3 : 1,
        "startTimeUnixNano": nanoString(startMs),
        "endTimeUnixNano": nanoString(startMs + max(0, durationMs)),
        "attributes": encodeAttributes([("openlog.rum.event", event)] + attributes),
    ]
    if let parentSpanID { span["parentSpanId"] = parentSpanID }
    if let exception {
        span["events"] = [[
            "timeUnixNano": nanoString(startMs),
            "name": "exception",
            "attributes": encodeAttributes([
                ("exception.type", exception.type),
                ("exception.message", truncate(exception.message, Limits.maxMessageBytes)),
                ("exception.stacktrace", truncate(exception.stacktrace, Limits.maxStackBytes)),
            ]),
        ]]
    }
    if isError { span["status"] = ["code": 2] }
    return span
}

/// The request body: one resource with its spans.
func buildPayload(spans: [[String: Any]], resource: [(String, Any?)]) -> [String: Any] {
    [
        "resourceSpans": [[
            "resource": ["attributes": encodeAttributes(resource)],
            "scopeSpans": [["spans": spans]],
        ]],
    ]
}
