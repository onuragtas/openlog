// The language itself: what it offers, and what is wrong with what was typed.
//
// The web's editor asks the server both questions -- `query/schema` for
// completion, `query/validate` for the underlines -- and this app asks the
// same two. A phone has no completion popup, so the schema feeds the guided
// builder instead; the validation is a list under the box, which is where the
// web puts it too.
import 'dart:async';

import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'oql_builder.dart';
import 'session.dart';

/// The event type written after FROM, as it was written, or an empty string.
///
/// A regular expression rather than the web's tokenizer: the only thing this
/// decides is which attribute list to ask for, so `FROM` inside a string
/// costing one wrong list is a fair trade against three hundred lines of
/// lexer. The server stays the one that parses.
String eventTypeOf(String query) {
  final m = RegExp(
    r'\bFROM\s+([A-Za-z_][A-Za-z0-9_]*)',
    caseSensitive: false,
  ).firstMatch(query);
  return m?.group(1) ?? '';
}

/// The canonical spelling of an event type, or an empty string when it is
/// not one of ours.
String canonicalEventType(String name) {
  if (name.isEmpty) return '';
  for (final e in oqlEventTypes) {
    if (e.toLowerCase() == name.toLowerCase()) return e;
  }
  return '';
}

/// What the language offers, for the builder's pickers.
///
/// Two requests, as the web makes: the plain schema for the event types and
/// the functions, and one per event type for that type's frequent map keys
/// and -- for Metric -- the metric names. The per-type answers are kept, so
/// moving back and forth between Log and Metric asks once each.
class OqlSchemaController extends ChangeNotifier {
  OqlSchemaController(this.client);

  final OpenlogClient client;

  OqlSchema? base;

  /// Per event type, as they arrive.
  final detail = <String, OqlSchema>{};

  bool loading = false;
  SessionFailure? failure;

  /// Asks for what is missing: the plain schema once, and [eventType] if it
  /// has not been asked for yet.
  Future<void> load({String eventType = ''}) async {
    final type = canonicalEventType(eventType);
    if (base != null && (type.isEmpty || detail.containsKey(type))) return;
    loading = true;
    failure = null;
    notifyListeners();
    try {
      base ??= await client.oqlSchema();
      if (type.isNotEmpty && !detail.containsKey(type)) {
        detail[type] = await client.oqlSchema(eventType: type);
      }
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = e.status == 403
          ? const SessionFailure('queryForbidden', '')
          : SessionFailure('unexpected', e.message);
    } finally {
      loading = false;
      notifyListeners();
    }
  }

  /// The attributes of one event type, as the schema lists them.
  List<OqlSchemaEventTypesItemAttributesItem> attributesOf(String eventType) {
    final type = canonicalEventType(eventType);
    for (final s in [detail[type], base]) {
      for (final e in s?.eventTypes ?? const []) {
        if (e.name == type) return e.attributes;
      }
    }
    return const [];
  }

  /// Every key that can be filtered or split by: the type's own attributes
  /// and the map keys the last hour actually had.
  List<String> keysOf(String eventType) {
    final type = canonicalEventType(eventType);
    final s = detail[type];
    return [
      for (final a in attributesOf(type)) a.name,
      for (final k in s?.attributeKeys ?? const []) 'attributes.$k',
      for (final k in s?.resourceKeys ?? const []) 'resource.$k',
    ];
  }

  /// The same, without the keys a number measure cannot be taken of.
  List<String> numberKeysOf(String eventType) {
    final type = canonicalEventType(eventType);
    final s = detail[type];
    return [
      for (final a in attributesOf(type))
        if (a.type == OqlSchemaEventTypesItemAttributesItemType.number) a.name,
      // A map value has no type in the schema: it is text in ClickHouse and
      // the server casts it, so these stay offered rather than hidden.
      for (final k in s?.attributeKeys ?? const []) 'attributes.$k',
      for (final k in s?.resourceKeys ?? const []) 'resource.$k',
    ];
  }

  List<String> metricNames() => detail['Metric']?.metricNames ?? const [];
}

/// What the server says is wrong with the query, before it is run.
///
/// Debounced here rather than in the screen: the question is "what does the
/// server think of what is in the box", and asking it on every keystroke
/// would be one request per letter.
class OqlValidationController extends ChangeNotifier {
  OqlValidationController(
    this.client, {
    this.delay = const Duration(milliseconds: 400),
  });

  final OpenlogClient client;
  final Duration delay;

  /// The query the result below belongs to, so a result is never shown
  /// against text that has already moved on.
  String checked = '';
  OqlValidation? result;
  bool checking = false;

  Timer? _timer;

  /// Validation that fails is not worth a banner: the person is typing, and
  /// the console's own Run button will say it properly.
  SessionFailure? failure;

  /// Ask again in a moment, unless the text changes first.
  void schedule(String query) {
    _timer?.cancel();
    final q = query.trim();
    if (q.isEmpty) {
      checked = '';
      result = null;
      notifyListeners();
      return;
    }
    if (q == checked) return;
    _timer = Timer(delay, () => check(q));
  }

  Future<void> check(String query) async {
    final q = query.trim();
    if (q.isEmpty) return;
    checking = true;
    notifyListeners();
    try {
      final answer = await client.validateQuery(q);
      result = answer;
      checked = q;
      failure = null;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = SessionFailure('unexpected', e.message);
      // An answer about another query is worse than none.
      result = null;
      checked = '';
    } finally {
      checking = false;
      notifyListeners();
    }
  }

  /// The errors and the warnings of the text that is on screen, errors
  /// first. Empty while the box has moved on from what was checked.
  List<({OqlDiagnostic diagnostic, bool error})> diagnosticsFor(String query) {
    final r = result;
    if (r == null || query.trim() != checked) return const [];
    return [
      for (final d in r.errors) (diagnostic: d, error: true),
      for (final d in r.warnings) (diagnostic: d, error: false),
    ];
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
  }
}
