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

/// One condition, in the shape the server's `filters` parameter takes.
class Filter {
  const Filter({required this.key, required this.op, this.values = const []});

  final String key;

  /// `eq`, `in`, `exists`… as the contract spells them.
  final String op;

  /// Empty for `exists` and `not_exists`, one for `eq`, several for `in`.
  final List<String> values;

  Map<String, Object?> toJson() => {
    'key': key,
    'op': op,
    if (values.length == 1 && op != 'in' && op != 'not_in')
      'value': values.first
    else if (values.isNotEmpty)
      'values': values,
  };

  /// What the chip says.
  String get label => switch (op) {
    'exists' => '$key ✓',
    'not_exists' => '$key ✗',
    'not_in' || 'neq' => '$key ≠ ${values.join(', ')}',
    _ => '$key = ${values.join(', ')}',
  };
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

  Future<void> loadValues(String forKey, {String q = ''}) async {
    loading = true;
    failure = null;
    key = forKey;
    notifyListeners();
    try {
      final page = await client.fieldValues(signal: signal, key: forKey, q: q);
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
