// What one log record is made of.
//
// A port of the field half of `web/src/lib/logs-explorer.ts`: the top-level
// fields of a row, its attributes, its resource attributes and -- when the
// body is a JSON object -- the body's own keys, each under the key a filter
// would use. One record is one flat list of key/value pairs, which is what
// makes "filter by this" and "show this as a column" the same gesture on
// every one of them.
import 'dart:convert';

import 'api/schema.g.dart';
import 'fields.dart';

/// The top-level fields, in the web's order. The value comes from the row
/// itself rather than from `fields`, which only carries the extra columns
/// that were asked for.
const _rowFields = <String, String Function(LogQueryRow)>{
  'timestamp': _timestamp,
  'observed_timestamp': _observed,
  'body': _body,
  'severity_text': _severityText,
  'severity_number': _severityNumber,
  'service.name': _serviceName,
  'host.id': _hostId,
  'host.name': _hostName,
  'trace_id': _traceId,
  'span_id': _spanId,
};

String _timestamp(LogQueryRow r) => r.timestamp.toUtc().toIso8601String();
String _observed(LogQueryRow r) =>
    r.observedTimestamp.toUtc().toIso8601String();
String _body(LogQueryRow r) => r.body;
String _severityText(LogQueryRow r) => r.severityText;
String _severityNumber(LogQueryRow r) => '${r.severityNumber}';
String _serviceName(LogQueryRow r) => r.serviceName;
String _hostId(LogQueryRow r) => r.hostId;
String _hostName(LogQueryRow r) => r.hostName;
String _traceId(LogQueryRow r) => r.traceId;
String _spanId(LogQueryRow r) => r.spanId;

/// True for a key the row carries by itself, so it is never asked for as a
/// column: `columns` is for the keys that are not already there.
bool isRowField(String key) => _rowFields.containsKey(key);

/// One line of the detail list.
class RecordField {
  const RecordField({
    required this.key,
    required this.value,
    required this.source,
  });

  /// The key a filter would use: `service.name`, `attributes.http.method`,
  /// `resource.container.id`, `body.level`.
  final String key;
  final String value;
  final FieldSource source;
}

/// Every field of a record, in the order the web's detail panel lists them:
/// the row's own fields, then the attributes and the resource attributes by
/// name, then the keys of a JSON body.
///
/// An empty value is left out -- a phone screen is not the place to scroll
/// past twenty blank lines -- and so is `severity_number` when it is 0,
/// which is what a record with no severity at all carries.
List<RecordField> recordFields(LogQueryRow row) {
  final out = <RecordField>[];
  for (final e in _rowFields.entries) {
    final value = e.value(row);
    if (value.isEmpty) continue;
    if (e.key == 'severity_number' && value == '0') continue;
    out.add(RecordField(key: e.key, value: value, source: FieldSource.field));
  }
  for (final e in _sorted(row.attributes)) {
    out.add(
      RecordField(
        key: 'attributes.${e.key}',
        value: e.value,
        source: FieldSource.attribute,
      ),
    );
  }
  for (final e in _sorted(row.resourceAttributes)) {
    out.add(
      RecordField(
        key: 'resource.${e.key}',
        value: e.value,
        source: FieldSource.resource,
      ),
    );
  }
  for (final e in (jsonBody(row.body) ?? const {}).entries) {
    out.add(
      RecordField(
        key: 'body.${e.key}',
        value: e.value is String ? e.value! as String : jsonEncode(e.value),
        source: FieldSource.body,
      ),
    );
  }
  return out;
}

List<MapEntry<String, String>> _sorted(Map<String, String>? m) =>
    [...?m?.entries]..sort((a, b) => a.key.compareTo(b.key));

/// The body parsed as a JSON object, or null. Structured logs are common
/// enough that their keys are worth listing as fields; anything else --
/// a plain line, a JSON array, a number -- is just a body.
Map<String, Object?>? jsonBody(String body) {
  final s = body.trim();
  if (!s.startsWith('{')) return null;
  try {
    final v = jsonDecode(s);
    return v is Map<String, Object?> ? v : null;
  } on FormatException {
    return null;
  }
}

/// What a column shows for a record, or null when the record has no such
/// key. The lookup order is the web's: the row's own fields, the columns
/// the server was asked for, then the attribute maps -- with and without
/// their prefix, because a key picked from the dictionary carries one and
/// a key typed by hand may not.
String? cellValue(LogQueryRow row, String key) {
  final own = _rowFields[key];
  if (own != null) return own(row);
  final asked = row.fields[key];
  if (asked != null) return asked;
  if (key.startsWith('attributes.')) {
    return row.attributes?[key.substring('attributes.'.length)];
  }
  if (key.startsWith('resource.')) {
    return row.resourceAttributes?[key.substring('resource.'.length)];
  }
  return row.attributes?[key] ?? row.resourceAttributes?[key];
}

/// Whether a value may be used in a condition at all: the server's limit
/// is 1024 bytes, and a stack trace in an attribute is past it.
bool isFilterableValue(String value) => utf8.encode(value).length <= 1024;

/// The whole record as JSON, indented, with the body expanded when it is
/// itself JSON -- which is what the web's detail panel shows in its second
/// tab, and what somebody pastes into a ticket.
String recordJson(LogQueryRow row) {
  final m = row.toJson()..remove('fields');
  final body = jsonBody(row.body);
  if (body != null) m['body'] = body;
  return const JsonEncoder.withIndent('  ').convert(m);
}

/// "Show me only this" / "hide this", as a condition.
///
/// A numeric key compares as a number: `severity_number = "17"` and
/// `severity_number = 17` are different questions to ClickHouse, and the
/// first one is the one that answers nothing.
Filter valueFilter(
  String key,
  String value, {
  bool exclude = false,
  FieldType? type,
}) => Filter(
  key: key,
  op: exclude ? '!=' : '=',
  values: [value],
  numeric:
      (type == FieldType.number || key == 'severity_number') &&
      value.trim().isNotEmpty &&
      num.tryParse(value.trim()) != null,
);
