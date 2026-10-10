// The dictionary a filter is built from.
//
// `fields/keys` says which keys the data in the range actually has, and
// `fields/values` says what each one holds, with counts. A phone needs this
// more than the web does: a key somebody has to remember and type is a key
// they will get wrong.
import 'dart:convert';

import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'session.dart';

/// Every operator the contract has, in the contract's own spelling.
///
/// `=` and not `eq`: the server checks the operator against this exact list
/// and answers 400 for anything else. This app sent `eq` for a while, which
/// made every single-value chip a bad request.
const filterOps = [
  '=',
  '!=',
  'in',
  'not_in',
  'contains',
  'not_contains',
  'like',
  'not_like',
  'regex',
  'not_regex',
  'exists',
  'not_exists',
  '>',
  '>=',
  '<',
  '<=',
];

const _noValueOps = {'exists', 'not_exists'};
const _multiValueOps = {'in', 'not_in'};

/// One condition, in the shape the server's `filters` parameter takes.
class Filter {
  const Filter({required this.key, required this.op, this.values = const []});

  /// Reads one out of a saved view's state, which is somebody else's JSON.
  ///
  /// Null for anything this app would not be able to send: an unknown
  /// operator, a missing value, a value that is not a scalar. A view made in
  /// a browser version this app does not know must drop the condition it
  /// cannot show rather than send it back as something else.
  static Filter? fromJson(Object? json) {
    if (json is! Map) return null;
    final key = json['key'];
    final op = json['op'];
    if (key is! String || key.isEmpty || key.length > 256) return null;
    if (op is! String || !filterOps.contains(op)) return null;
    if (_noValueOps.contains(op)) return Filter(key: key, op: op);
    final raw = json['values'] ?? json['value'];
    // Numbers and booleans come back as text, which is what the server does
    // with them too: it reads every scalar as its text before building SQL.
    final values = <String>[];
    for (final v in raw is List ? raw : [raw]) {
      if (v is String) {
        values.add(v);
      } else if (v is num || v is bool) {
        values.add('$v');
      } else {
        return null;
      }
    }
    if (values.isEmpty || values.length > 100) return null;
    if (!_multiValueOps.contains(op) && values.length != 1) return null;
    return Filter(key: key, op: op, values: values);
  }

  final String key;

  /// `=`, `in`, `exists`… as [filterOps] spells them.
  final String op;

  /// Empty for `exists` and `not_exists`, one for `=`, several for `in`.
  final List<String> values;

  Map<String, Object?> toJson() => {
    'key': key,
    'op': op,
    if (values.length == 1 && !_multiValueOps.contains(op))
      'value': values.first
    else if (values.isNotEmpty)
      'values': values,
  };

  /// What the chip says.
  String get label => switch (op) {
    'exists' => '$key ✓',
    'not_exists' => '$key ✗',
    'not_in' || '!=' => '$key ≠ ${values.join(', ')}',
    '=' || 'in' => '$key = ${values.join(', ')}',
    // contains, like, regex, >, >=, <, <=: the operator itself is the
    // clearest label there is, and it is one somebody chose in a browser.
    _ => '$key $op ${values.join(', ')}',
  };
}

/// The conditions of a saved view's `filters`, dropping what cannot be shown.
List<Filter> filtersFromJson(Object? json) {
  if (json is! List) return const [];
  final out = <Filter>[];
  for (final e in json) {
    final f = Filter.fromJson(e);
    // Fifty conditions is the server's limit; a longer list is somebody
    // else's bug and the server would refuse the lot.
    if (f != null && out.length < 50) out.add(f);
  }
  return out;
}

/// The `filters` query parameter: a JSON array, or empty when there is
/// nothing to say.
String encodeFilters(List<Filter> filters) =>
    filters.isEmpty ? '' : jsonEncode([for (final f in filters) f.toJson()]);

/// The keys and values of one signal, for the builder sheet.
class FieldsController extends ChangeNotifier {
  FieldsController(this.client, {required this.signal});

  final OpenlogClient client;

  /// `logs`, `traces` or `metrics`.
  final String signal;

  List<FieldKey> keys = const [];
  List<FieldValue> values = const [];

  /// The key whose values are on screen, or empty while choosing one.
  String key = '';

  /// True when the answer came from a sample rather than the index: the
  /// list is then the most frequent, not all of them.
  bool keysSampled = false;
  bool valuesSampled = false;

  bool loading = false;
  SessionFailure? failure;

  Future<void> loadKeys({String q = ''}) async {
    loading = true;
    failure = null;
    key = '';
    values = const [];
    notifyListeners();
    try {
      final page = await client.fieldKeys(signal: signal, q: q);
      keys = page.keys;
      keysSampled = page.sampled;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  /// [metric] narrows a metric's attribute values to that metric: every
  /// metric shares the attribute table, so without it `host.name` would
  /// list the hosts of all of them.
  Future<void> loadValues(
    String forKey, {
    String q = '',
    String metric = '',
  }) async {
    loading = true;
    failure = null;
    key = forKey;
    notifyListeners();
    try {
      final page = await client.fieldValues(
        signal: signal,
        key: forKey,
        q: q,
        metric: metric,
      );
      values = page.values;
      valuesSampled = page.sampled;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  SessionFailure _failureOf(ApiException e) => switch (e.status) {
    403 => const SessionFailure('sectionForbidden', ''),
    _ => SessionFailure('unexpected', e.message),
  };
}
