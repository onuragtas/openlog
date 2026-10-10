// The guided query builder: picks become OQL text.
//
// A port of `web/src/lib/oql-builder.ts`, line for line, because the two have
// to write the same query for the same picks -- a query built on a phone and
// the same one built in a browser must be the same text, or "it works on the
// web" becomes a thing people say about this app.
//
// One way only: it writes OQL, it never reads it back.
library;

/// The event types OQL can read, in the web's order.
const oqlEventTypes = [
  'Log',
  'Span',
  'Transaction',
  'Metric',
  'Host',
  'Container',
];

/// What to show: `count` needs no attribute, the rest do.
const builderMeasures = [
  'count',
  'average',
  'sum',
  'min',
  'max',
  'uniqueCount',
  'median',
  'percentile',
  'latest',
];

/// Measures that need a number attribute; the others take any.
const numberMeasures = ['average', 'sum', 'min', 'max', 'median', 'percentile'];

const builderOps = [
  '=',
  '!=',
  'contains',
  'like',
  '>',
  '>=',
  '<',
  '<=',
  'is_null',
  'is_not_null',
];

const maxGroupBy = 5;
const maxConditions = 10;

/// One condition of the builder.
class BuilderCondition {
  BuilderCondition({this.key = '', this.op = '=', this.value = ''});

  /// A qualified key: `service.name`, `attributes.<k>` or `resource.<k>`.
  String key;
  String op;
  String value;

  BuilderCondition copy() => BuilderCondition(key: key, op: op, value: value);
}

/// Everything the builder knows, which is everything the query says.
class BuilderState {
  BuilderState({
    this.eventType = 'Log',
    this.measure = 'count',
    String? attribute,
    this.metricName = '',
    List<BuilderCondition>? conditions,
    List<String>? groupBy,
    this.timeseries = true,
    this.limit = 10,
  }) : attribute = attribute ?? (eventType == 'Metric' ? 'value' : ''),
       conditions = conditions ?? [],
       groupBy = groupBy ?? [];

  String eventType;
  String measure;

  /// The attribute the measure runs on; every measure except `count`.
  String attribute;

  /// Metric event type: which metric to read.
  String metricName;
  List<BuilderCondition> conditions;
  List<String> groupBy;
  bool timeseries;
  int limit;
}

String _quote(String s) =>
    "'${s.replaceAll('\\', '\\\\').replaceAll("'", "''")}'";

final _numeric = RegExp(r'^-?\d+(\.\d+)?$');

/// The OQL form of a qualified key: `attributes.k`, `attr.k` and
/// `resource.k` become map lookups, everything else stays as it is.
String oqlKey(String key) {
  const prefixes = {
    'attributes.': 'attributes',
    'attr.': 'attributes',
    'resource.': 'resource',
  };
  for (final e in prefixes.entries) {
    if (key.startsWith(e.key) && key.length > e.key.length) {
      return '${e.value}[${_quote(key.substring(e.key.length))}]';
    }
  }
  return key;
}

/// The select expression of a measure: `count(*)`, `average(duration.ms)`,
/// `percentile(duration.ms, 50, 95, 99)`.
String measureExpression(String measure, String attribute) {
  if (measure == 'count') return 'count(*)';
  final attr = oqlKey(attribute);
  return measure == 'percentile'
      ? 'percentile($attr, 50, 95, 99)'
      : '$measure($attr)';
}

String? _condition(BuilderCondition c) {
  final key = oqlKey(c.key);
  if (c.op == 'is_null') return '$key IS NULL';
  if (c.op == 'is_not_null') return '$key IS NOT NULL';
  final raw = c.value.trim();
  if (raw.isEmpty) return null;
  // Numbers and booleans go in unquoted; a map value is a string, so a
  // quoted number still matches there.
  final literal = _numeric.hasMatch(raw) || raw == 'true' || raw == 'false'
      ? raw
      : _quote(raw);
  return switch (c.op) {
    'contains' => '$key CONTAINS ${_quote(raw)}',
    'like' => '$key LIKE ${_quote(raw)}',
    _ => '$key ${c.op} $literal',
  };
}

/// `attribute` or `metric` when the picks cannot make a query yet, else
/// null. The screen says which, rather than offering a dead button.
String? builderIssue(BuilderState s) {
  if (s.measure != 'count' && s.attribute.trim().isEmpty) return 'attribute';
  if (s.eventType == 'Metric' && s.metricName.trim().isEmpty) return 'metric';
  return null;
}

/// The OQL of these picks, or an empty string while they are incomplete.
String buildOql(BuilderState s) {
  if (builderIssue(s) != null) return '';
  final conds = <String>[];
  if (s.eventType == 'Metric' && s.metricName.trim().isNotEmpty) {
    conds.add('metricName = ${_quote(s.metricName.trim())}');
  }
  for (final c in s.conditions.take(maxConditions)) {
    if (c.key.trim().isEmpty) continue;
    final text = _condition(c);
    if (text != null) conds.add(text);
  }
  final groups = s.groupBy
      .where((g) => g.trim().isNotEmpty)
      .take(maxGroupBy)
      .toList();
  var q =
      'SELECT ${measureExpression(s.measure, s.attribute)} '
      'FROM ${s.eventType}';
  if (conds.isNotEmpty) q += ' WHERE ${conds.join(' AND ')}';
  if (groups.isNotEmpty) {
    q +=
        ' FACET ${groups.map(oqlKey).join(', ')} '
        'LIMIT ${s.limit < 1 ? 1 : s.limit}';
  }
  if (s.timeseries) q += ' TIMESERIES AUTO';
  return q;
}
