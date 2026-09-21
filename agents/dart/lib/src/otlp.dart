// OTLP/JSON encoding (docs/contracts/mobile-agent.md §3).
//
// Hand-written rather than generated from the protobuf: the payload is four nested objects and a list of
// attributes, and a code generator plus its runtime would be the dependency this package exists without.

import 'limits.dart';

/// The event kinds the server accepts. A span without one is not RUM and is dropped.
class RumEvent {
  static const String screen = 'page_view';
  static const String vital = 'vital';
  static const String error = 'error';
  static const String resource = 'resource';
  static const String custom = 'custom';
}

/// Nanoseconds as a string: milliseconds are integers here, and concatenation avoids the rounding that
/// `ms * 1e6` introduces past 2^53.
String _nano(int ms) => '${ms < 0 ? 0 : ms}000000';

/// Every attribute travels as a string: the backend stores span attributes as Map(String, String).
List<Map<String, Object>> encodeAttributes(Map<String, Object?> attrs) {
  final out = <Map<String, Object>>[];
  for (final e in attrs.entries) {
    final v = e.value;
    if (v == null) continue;
    final s = v is String ? v : '$v';
    if (s.isEmpty) continue; // an empty attribute is not a value, and the server drops it anyway
    if (out.length >= maxAttributesPerSpan) break;
    out.add({
      'key': e.key,
      'value': {'stringValue': truncate(s, e.key == 'url.full' ? maxUrlBytes : maxAttrValueBytes)},
    });
  }
  return out;
}

/// One span of a session.
Map<String, Object?> buildSpan({
  required String name,
  required String event,
  required String traceId,
  required String spanId,
  required int startMs,
  int durationMs = 0,
  String? parentSpanId,
  Map<String, Object?> attributes = const {},
  bool isError = false,
  Map<String, String>? exception,
}) {
  final span = <String, Object?>{
    'traceId': traceId,
    'spanId': spanId,
    if (parentSpanId != null) 'parentSpanId': parentSpanId,
    'name': truncate(name, maxNameBytes),
    // INTERNAL for screens, vitals and errors; CLIENT for requests, like the browser SDK.
    'kind': event == RumEvent.resource ? 3 : 1,
    'startTimeUnixNano': _nano(startMs),
    'endTimeUnixNano': _nano(startMs + (durationMs < 0 ? 0 : durationMs)),
    'attributes': encodeAttributes({'openlog.rum.event': event, ...attributes}),
  };
  if (exception != null) {
    span['events'] = [
      {
        'timeUnixNano': _nano(startMs),
        'name': 'exception',
        'attributes': encodeAttributes({
          'exception.type': exception['type'],
          'exception.message': truncate(exception['message'] ?? '', maxMessageBytes),
          'exception.stacktrace': truncate(exception['stacktrace'] ?? '', maxStackBytes),
        }),
      }
    ];
  }
  if (isError) span['status'] = {'code': 2};
  return span;
}

/// The request body: one resource with its spans.
Map<String, Object?> buildPayload(List<Map<String, Object?>> spans, Map<String, Object?> resource) => {
      'resourceSpans': [
        {
          'resource': {'attributes': encodeAttributes(resource)},
          'scopeSpans': [
            {'spans': spans}
          ],
        }
      ],
    };
