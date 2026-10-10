// Generates Dart data classes from docs/contracts/openapi.yaml into
// lib/src/api/schema.g.dart, so a change to the server contract breaks this
// build instead of surfacing as a runtime cast on someone's phone.
//
//   dart run tool/gen_api.dart           # write the file
//   dart run tool/gen_api.dart --check   # fail if the file is out of date (CI)
//
// Why a generator of our own rather than an off-the-shelf one: the spec is
// OpenAPI 3.1 with 108 `type: [x, "null"]` unions and 39 `oneOf`s.
// swagger_parser 1.45.0 crashes on it -- `type 'List<Object?>' is not a subtype
// of type 'Map<String, dynamic>' in type cast`, 0 files generated -- because it
// reads `type` as a string. openapi-generator's dart targets want a JVM in the
// mobile CI job and treat 3.1 as experimental. Both would also emit all 278
// endpoints when the app parses about ten, and a generator that silently falls
// back to `dynamic` for the constructs this spec is full of would leave the
// guard above claiming a safety it does not provide.
//
// So this handles exactly what the spec uses and *fails loudly* on anything
// else. If it stops on a construct, teach it the construct -- do not widen a
// field to `dynamic`.
import 'dart:io';

import 'package:yaml/yaml.dart';

/// What to generate. Only what the app parses, resolved transitively through
/// `$ref`, so the file stays the size of the app's needs rather than the API's.
const schemaTargets = <String>[
  'AuthConfig', // the server handshake: is this an openlog server, and what does its sign-in screen look like
  'Me', // who am I, which organizations, which role
  'Session', // the person's sessions, including other phones
  'AlertIncident', // what the app exists to show: what is firing right now
  'AlertIncidentDetail', // one incident with its timeline and what was delivered
  'AlertRule', // the rules behind the incidents, and whether they are on
  'AlertChannelTestResult', // whether a channel would actually reach anyone
  'AlertMute', // what is silenced, and until when
  'AlertRoutingRule', // where a page goes, and why it went there
  'AlertHolidayCalendar', // the dated lists a recurring mute skips
  'AlertTemplate', // the recommended rules, which is how a rule gets made on a phone
  'AlertTemplateRender', // a template plus its values, as a rule nobody has stored yet
  'AlertRulePreview', // what that rule would have done over the last few hours
  'ApmService', // where to look after an alert: which service, how healthy
  'ApmOverview', // that service's golden signals, so the alert gets a shape
  'ApmErrorInbox', // what is actually breaking in that service
  'Trace', // one request end to end: where the time and the error went
  'TracesQueryResponse', // the span search behind the traces section
  'MetricListResponse', // the metrics explorer's list
  'MetricDetail', // one metric: what it is and how it can be aggregated
  'MetricQueryResponse', // and the series to draw
  'RumOverview', // what the browser saw: Core Web Vitals and page views
  'FleetSummary', // how far behind the agents are
  'Onboarding', // the endpoints and versions an SDK has to be pointed at
  'InventoryResponse', // a host's own snapshot, which is what runs on it
  'ContainerDetail', // one container, with the attributes the list leaves out
  'ContainerTimeseries', // and what it has been doing
  'KubernetesPodDetail', // one pod, its containers and the services on it
  'KubernetesPodTimeseries', // what it has been doing
  'KubernetesEventList', // and why it is not doing it, when it is not
  'DiscoveredService', // the body of a discovered_service inventory item,
  // which is where an integration reports whether it is collecting
  'DashboardSummary', // the dashboard list
  'Dashboard', // one dashboard with its pages and widgets
  'OqlResult', // what a widget's query answers
];

/// Schema names that would collide with something Flutter already owns.
///
/// The contract is free to call a thing `Container`; a Dart file that imports
/// material.dart is not. Renaming here rather than prefixing the import keeps
/// every call site reading as itself.
const renames = <String, String>{'Container': 'ApiContainer'};

/// The Dart name for a schema.
String dartName(String schema) => renames[schema] ?? schema;

/// Responses that are declared inline on a path rather than as a named schema.
/// 'METHOD path status ClassName'.
const responseTargets = <String>[
  'post /api/v1/auth/device 201 DeviceSession',
  'get /api/v1/alerts/incidents 200 IncidentPage',
  'get /api/v1/alerts/rules 200 AlertRulePage',
  'get /api/v1/alerts/channels 200 AlertChannelPage',
  'get /api/v1/alerts/mutes 200 AlertMutePage',
  'get /api/v1/alerts/routing-rules 200 AlertRoutingRulePage',
  'get /api/v1/alerts/holiday-calendars 200 AlertHolidayCalendarPage',
  'get /api/v1/alerts/deliveries 200 AlertDeliveryPage',
  'get /api/v1/alerts/templates 200 AlertTemplatePage',
  'get /api/v1/alerts/rule-types 200 AlertRuleTypePage',
  'get /api/v1/apm/errors/groups/{group_id}/comments 200 ApmErrorCommentPage',
  'get /api/v1/sessions 200 SessionPage',
  'get /api/v1/apm/services 200 ServicePage',
  'get /api/v1/logs 200 LogPage',
  'get /api/v1/rum/apps 200 RumAppPage',
  'get /api/v1/costs/hosts 200 CostHostPage',
  'get /api/v1/inventory/search 200 InventoryPage',
  'get /api/v1/fleet/hosts 200 FleetHostPage',
  'get /api/v1/profiles/services 200 ProfileServicePage',
  'get /api/v1/profiles/functions 200 ProfileFunctionPage',
  'get /api/v1/dashboards 200 DashboardPageList',
  'get /api/v1/hosts 200 HostPage',
  'get /api/v1/containers 200 ContainerPage',
  'get /api/v1/kubernetes/pods 200 PodPage',
  'get /api/v1/slos 200 SloPage',
  'get /api/v1/synthetics/checks 200 SyntheticPage',
  'get /api/v1/jobs/monitors 200 JobMonitorPage',
  'get /api/v1/vulnerabilities 200 VulnPage',
  'get /api/v1/db/instances 200 DbInstancePage',
];

void main(List<String> args) {
  final check = args.contains('--check');
  final specFile = File('../docs/contracts/openapi.yaml');
  if (!specFile.existsSync()) {
    stderr.writeln(
      'cannot find ${specFile.path}: run this from the mobile/ directory',
    );
    exit(2);
  }
  final spec = loadYaml(specFile.readAsStringSync()) as YamlMap;
  final out = _formatted(Generator(spec).run());

  final target = File('lib/src/api/schema.g.dart');
  if (check) {
    final current = target.existsSync() ? target.readAsStringSync() : '';
    if (current == out) {
      stdout.writeln('schema.g.dart is up to date');
      return;
    }
    stderr.writeln(
      'lib/src/api/schema.g.dart is out of date with '
      'docs/contracts/openapi.yaml.\nRun: dart run tool/gen_api.dart',
    );
    exit(1);
  }
  target.writeAsStringSync(out);
  stdout.writeln('wrote ${target.path}');
}

/// Runs the output through `dart format`, so the committed file is formatted
/// like every other file and `dart format --set-exit-if-changed` in CI needs no
/// exception for it -- an exception that had to be written as a `find` in the
/// workflow, which is its own small problem. Emitting formatted code by hand
/// would mean guessing at the formatter's line breaking; shelling out to the
/// SDK's own formatter means the two cannot disagree about a version either.
String _formatted(String source) {
  final tmp = Directory.systemTemp.createTempSync('openlog_gen_api');
  try {
    final file = File('${tmp.path}/schema.g.dart')..writeAsStringSync(source);
    final r = Process.runSync('dart', ['format', file.path]);
    if (r.exitCode != 0) {
      stderr.writeln('gen_api: dart format failed: ${r.stderr}');
      exit(2);
    }
    return file.readAsStringSync();
  } finally {
    tmp.deleteSync(recursive: true);
  }
}

/// A Dart type and whether the field holding it may be null.
class DartType {
  const DartType(this.name, {this.nullable = false});
  final String name;
  final bool nullable;
  String get decl => nullable ? '$name?' : name;
}

class Generator {
  Generator(this.spec);

  final YamlMap spec;
  final _classes =
      <String, String>{}; // name -> source, emitted in insertion order
  final _enums = <String, String>{};
  final _pending = <String>[];
  final _done = <String>{};

  YamlMap get _schemas => (spec['components'] as YamlMap)['schemas'] as YamlMap;

  String run() {
    for (final t in schemaTargets) {
      _queue(t);
    }
    final responses = <String, YamlMap>{};
    for (final t in responseTargets) {
      final parts = t.split(' ');
      if (parts.length != 4) {
        _fail('responseTargets entry is not "method path status Class": $t');
      }
      responses[parts[3]] = _responseSchema(parts[0], parts[1], parts[2]);
    }
    void drain() {
      while (_pending.isNotEmpty) {
        final name = _pending.removeAt(0);
        if (!_done.add(name)) continue;
        final node = _schemas[name];
        if (node == null) _fail('schema $name is not in components.schemas');
        _emit(dartName(name), node as YamlMap);
      }
    }

    drain();
    // Inline response bodies after the named schemas they reference.
    responses.forEach(_emit);
    // And again: a response body can be the only thing that reaches a schema.
    // LogPage is the whole reason -- its `logs` array is the only reference to
    // LogRecord anywhere in the targets, and emitting responses last left that
    // reference queued and never drained, which the generated file reported as
    // "LogRecord isn't a type" rather than as anything about the generator.
    drain();

    final b = StringBuffer()
      ..writeln(
        '// GENERATED by tool/gen_api.dart from docs/contracts/openapi.yaml. Do not edit.',
      )
      ..writeln('//')
      ..writeln('// Regenerate with: dart run tool/gen_api.dart')
      ..writeln(
        '// CI fails when this file and the contract disagree (dart run tool/gen_api.dart --check).',
      )
      ..writeln()
      // The readers below are a fixed prelude; which of them a given set of
      // targets happens to need changes as the targets do, and emitting only the
      // used ones would churn this file for no gain.
      ..writeln(
        '// ignore_for_file: lines_longer_than_80_chars, unused_element',
      )
      ..writeln()
      ..writeln(_header);
    for (final src in _enums.values) {
      b
        ..writeln()
        ..writeln(src);
    }
    for (final src in _classes.values) {
      b
        ..writeln()
        ..writeln(src);
    }
    return b.toString();
  }

  static const _header = '''
/// Thrown when the server sends a body this build cannot read: a required field
/// is missing or has the wrong shape. It names the field, because "unexpected
/// null" three screens later is how a contract break turns into a bug report
/// about something else.
class ApiShapeError implements Exception {
  ApiShapeError(this.path, this.detail);

  final String path;
  final String detail;

  @override
  String toString() => 'the server sent something this app cannot read at "\$path": \$detail';
}

Map<String, Object?> _obj(Object? v, String path) {
  if (v is Map) return v.cast<String, Object?>();
  throw ApiShapeError(path, 'expected an object, got \${v.runtimeType}');
}

T _req<T>(Map<String, Object?> m, String key, String path, T Function(Object?, String) read) {
  if (!m.containsKey(key) || m[key] == null) {
    throw ApiShapeError('\$path.\$key', 'required field is missing');
  }
  return read(m[key], '\$path.\$key');
}

T? _opt<T>(Map<String, Object?> m, String key, String path, T Function(Object?, String) read) {
  final v = m[key];
  return v == null ? null : read(v, '\$path.\$key');
}

String _str(Object? v, String path) {
  if (v is String) return v;
  throw ApiShapeError(path, 'expected a string, got \${v.runtimeType}');
}

bool _bool(Object? v, String path) {
  if (v is bool) return v;
  throw ApiShapeError(path, 'expected a boolean, got \${v.runtimeType}');
}

int _int(Object? v, String path) {
  if (v is int) return v;
  if (v is num && v == v.roundToDouble()) return v.toInt();
  throw ApiShapeError(path, 'expected an integer, got \${v.runtimeType}');
}

double _num(Object? v, String path) {
  if (v is num) return v.toDouble();
  throw ApiShapeError(path, 'expected a number, got \${v.runtimeType}');
}

/// RFC3339 with nanoseconds. DateTime keeps microseconds, so the last three
/// digits are dropped; everything here is for display, never for equality.
DateTime _time(Object? v, String path) {
  final s = _str(v, path);
  final t = DateTime.tryParse(s);
  if (t == null) {
    throw ApiShapeError(path, 'expected an RFC3339 timestamp, got "\$s"');
  }
  return t.toUtc();
}

/// A value the contract itself declares as more than one scalar type.
Object? _any(Object? v, String path) => v;

/// Lets a reader stand in for an element that may be null.
///
/// A null inside a list is not a missing field: an OQL time series writes one
/// for a bucket with no data, and reading it with the plain number reader threw
/// where the contract says null is expected.
T? Function(Object?, String) _nullable<T>(T Function(Object?, String) read) =>
    (v, p) => v == null ? null : read(v, p);

List<T> _list<T>(Object? v, String path, T Function(Object?, String) read) {
  if (v is! List) {
    throw ApiShapeError(path, 'expected an array, got \${v.runtimeType}');
  }
  return List<T>.generate(
    v.length,
    (i) => read(v[i], '\$path[\$i]'),
    growable: false,
  );
}

Map<String, T> _map<T>(Object? v, String path, T Function(Object?, String) read) {
  final m = _obj(v, path);
  return {for (final e in m.entries) e.key: read(e.value, '\$path.\${e.key}')};
}

/// A fixed-length array the contract declares positionally (`prefixItems`),
/// such as an alert preview's `[unix ms, value]`.
///
/// A record rather than a generated class: the contract gives the positions no
/// names, and a class would have to invent two.
(A, B) _tuple2<A, B>(
  Object? v,
  String path,
  A Function(Object?, String) a,
  B Function(Object?, String) b,
) {
  if (v is! List) {
    throw ApiShapeError(path, 'expected an array, got \${v.runtimeType}');
  }
  if (v.length != 2) {
    throw ApiShapeError(path, 'expected an array of 2, got \${v.length}');
  }
  return (a(v[0], '\$path[0]'), b(v[1], '\$path[1]'));
}''';

  void _queue(String name) {
    if (!_done.contains(name) && !_pending.contains(name)) _pending.add(name);
  }

  Never _fail(String why) {
    stderr.writeln('gen_api: $why');
    stderr.writeln(
      'Teach tool/gen_api.dart the construct rather than widening a field to dynamic.',
    );
    exit(2);
  }

  YamlMap _responseSchema(String method, String path, String status) {
    final paths = spec['paths'] as YamlMap;
    final p = paths[path];
    if (p == null) _fail('path $path is not in the spec');
    final op = (p as YamlMap)[method];
    if (op == null) _fail('$method is not defined on $path');
    final res = ((op as YamlMap)['responses'] as YamlMap)[status];
    if (res == null) _fail('$method $path has no $status response');
    final content = (res as YamlMap)['content'];
    if (content == null) _fail('$method $path $status has no content');
    final json = (content as YamlMap)['application/json'];
    if (json == null) _fail('$method $path $status is not application/json');
    final schema = (json as YamlMap)['schema'];
    if (schema == null) _fail('$method $path $status has no schema');
    return schema as YamlMap;
  }

  /// Strips the three ways this spec says "nullable": a `oneOf` or an `anyOf`
  /// of one non-null branch plus `type: "null"`, and a `type: [x, "null"]`
  /// union. Returns the branch and that it is nullable.
  ///
  /// `anyOf` and `oneOf` mean different things in general -- "at least one" and
  /// "exactly one" -- but with a single real branch beside a null they say the
  /// same thing, which is the only shape accepted here. Anything wider still
  /// stops, because a union this cannot name is a field it would have to widen
  /// to `dynamic`.
  (YamlMap node, bool nullable) _unwrapNullable(YamlMap node) {
    var current = node;
    var nullable = false;

    // A loop, because these wrappers nest: SloListItem.status is an `allOf`
    // of one $ref carrying `nullable: true`, which is how OpenAPI 3.0 makes a
    // reference nullable (a $ref could not have siblings there) and which this
    // 3.1 spec still uses in places.
    while (true) {
      if (current['nullable'] == true) {
        nullable = true;
        final copy = Map<String, Object?>.of(current.cast<String, Object?>())
          ..remove('nullable');
        current = YamlMap.wrap(copy);
        continue;
      }

      // `allOf` of a single branch is a wrapper, not a composition; a real
      // composition has more than one and is flattened by _absorb instead.
      //
      // Unless it has siblings that carry schema of their own. ApmErrorGroup
      // is `type: object` with a one-branch `allOf` next to its own
      // `properties` and `required`, which is still a composition -- unwrapping
      // to the branch threw its fifteen fields away and left a bare $ref that
      // _emit then rejected as "not an object".
      final all = current['allOf'];
      if (all is YamlList &&
          all.length == 1 &&
          current['properties'] == null &&
          current['required'] == null &&
          current['type'] == null) {
        current = all.first as YamlMap;
        continue;
      }

      var unwrapped = false;
      for (final key in const ['oneOf', 'anyOf']) {
        final union = current[key];
        if (union is! YamlList) continue;
        final branches = union.cast<YamlMap>().toList();
        final nulls = branches.where((b) => b['type'] == 'null').toList();
        final rest = branches.where((b) => b['type'] != 'null').toList();
        if (nulls.length == 1 && rest.length == 1) {
          current = rest.single;
          nullable = true;
          unwrapped = true;
          break;
        }
        _fail(
          '$key with ${branches.length} branches is not supported: $current',
        );
      }
      if (unwrapped) continue;

      final type = current['type'];
      if (type is YamlList) {
        final types = type.cast<String>().toList();
        final rest = types.where((t) => t != 'null').toList();
        if (types.contains('null') && rest.length == 1) {
          final copy = Map<String, Object?>.of(current.cast<String, Object?>())
            ..['type'] = rest.single;
          return (YamlMap.wrap(copy), true);
        }
        // A union of scalars is the one place a value really is dynamic, and
        // the contract says so rather than this generator guessing: OqlValue
        // is `[number, string, "null"]` because a query column holds either.
        // It reads as Object?, and the app turns it back into something typed
        // where it is used. Anything wider still stops.
        const scalars = {'number', 'integer', 'string', 'boolean'};
        if (rest.every(scalars.contains)) {
          final copy = Map<String, Object?>.of(current.cast<String, Object?>())
            ..['type'] = '__any';
          return (YamlMap.wrap(copy), nullable || types.contains('null'));
        }
        _fail('type union $types is not supported');
      }

      return (current, nullable);
    }
  }

  DartType _type(YamlMap raw, String context) {
    final (node, nullable) = _unwrapNullable(raw);

    final ref = node[r'$ref'];
    if (ref is String) {
      const prefix = '#/components/schemas/';
      if (!ref.startsWith(prefix)) {
        _fail('only component schema refs are supported, got $ref');
      }
      final name = ref.substring(prefix.length);
      final target = _schemas[name];
      if (target == null) _fail('ref to unknown schema $name');
      // Aliases carry no identity of their own: a Timestamp is a DateTime, and
      // a NullableTimestamp is a nullable one. Generating a class for them
      // would make every call site unwrap a one-field box.
      final aliased = _aliasOf(dartName(name), target as YamlMap);
      if (aliased != null) {
        return DartType(aliased.name, nullable: nullable || aliased.nullable);
      }
      // A referenced enum takes the schema's own name, so Role is `Role`
      // everywhere rather than OrgRefRole in one place and MeRole in another.
      final (inner, innerNullable) = _unwrapNullable(target);
      if (inner['type'] == 'string' && inner['enum'] != null) {
        _emitEnum(
          dartName(name),
          (inner['enum'] as YamlList).cast<String>().toList(),
        );
        return DartType(dartName(name), nullable: nullable || innerNullable);
      }
      _queue(name);
      return DartType(dartName(name), nullable: nullable);
    }

    final type = node['type'];
    switch (type) {
      case 'string':
        if (node['enum'] != null) {
          final name = _enumName(context);
          _emitEnum(name, (node['enum'] as YamlList).cast<String>().toList());
          return DartType(name, nullable: nullable);
        }
        if (node['format'] == 'date-time') {
          return DartType('DateTime', nullable: nullable);
        }
        return DartType('String', nullable: nullable);
      case 'boolean':
        return DartType('bool', nullable: nullable);
      case 'integer':
        return DartType('int', nullable: nullable);
      case 'number':
        return DartType('double', nullable: nullable);
      case '__any':
        // Always nullable: a value that may be a number or a string is read
        // through a type test anyway, and a non-null Object would only move
        // the null check somewhere worse.
        return const DartType('Object', nullable: true);
      case 'array':
        final items = node['items'];
        // A positional array: `prefixItems` names types by position and
        // nothing else, so it becomes a Dart record of the same shape. Only a
        // fixed length, because a record has one.
        final prefix = node['prefixItems'];
        if (items == null && prefix is YamlList) {
          final min = node['minItems'];
          final max = node['maxItems'];
          if (min != prefix.length || max != prefix.length) {
            _fail(
              'prefixItems of ${prefix.length} without matching '
              'minItems/maxItems at $context',
            );
          }
          if (prefix.length != 2) {
            _fail('prefixItems of ${prefix.length} at $context: only 2 so far');
          }
          final parts = [
            for (var i = 0; i < prefix.length; i++)
              _type(prefix[i] as YamlMap, '$context${i + 1}'),
          ];
          return DartType(
            '(${[for (final p in parts) p.decl].join(', ')})',
            nullable: nullable,
          );
        }
        if (items == null) _fail('array without items at $context');
        final inner = _type(items as YamlMap, '${context}Item');
        return DartType('List<${inner.decl}>', nullable: nullable);
      case 'object':
        final extra = node['additionalProperties'];
        if (node['properties'] == null && extra is YamlMap) {
          final inner = _type(extra, '${context}Value');
          return DartType('Map<String, ${inner.decl}>', nullable: nullable);
        }
        // `additionalProperties: true` with no declared properties: an open bag
        // whose values the contract deliberately does not constrain, like an
        // incident event's `details`. A class would have no fields at all; the
        // honest Dart type is the map it is.
        if (node['properties'] == null && extra == true) {
          return DartType('Map<String, Object?>', nullable: nullable);
        }
        final name = _className(context);
        _emit(name, node);
        return DartType(name, nullable: nullable);
      default:
        // A node carrying nothing but prose is a value the contract leaves
        // open on purpose -- an inventory item's `data` is "the category's
        // JSON body; non-JSON bodies are returned as a string". Only when
        // there is genuinely no schema in it: a node with a misspelled `type`
        // still has a key this does not know, and still stops here.
        const annotations = {
          'description',
          'title',
          'example',
          'examples',
          'default',
          'deprecated',
          'readOnly',
          'writeOnly',
        };
        if (node.keys.every((k) => annotations.contains(k))) {
          return const DartType('Object', nullable: true);
        }
        _fail('unsupported type ${type ?? '(absent)'} at $context');
    }
  }

  /// Named schemas with no identity worth a class: a scalar, a list or a map.
  ///
  /// Timestamp is a DateTime, ApmNullableNumber is a `double?`, AlertLabels is
  /// a `Map<String, String>` and MetricPoint is a `List<double>`. Wrapping any
  /// of them in a class would make every call site unwrap a one-field box.
  DartType? _aliasOf(String name, YamlMap node) {
    final (inner, nullable) = _unwrapNullable(node);

    final ref = inner[r'$ref'];
    if (ref is String && nullable) {
      final t = _type(YamlMap.wrap({r'$ref': ref}), name);
      return DartType(t.name, nullable: true);
    }
    if (inner['properties'] != null || inner['enum'] != null) return null;

    switch (inner['type']) {
      case 'string':
        if (inner['format'] == 'date-time') {
          return DartType('DateTime', nullable: nullable);
        }
        return DartType('String', nullable: nullable);
      case 'integer':
        return DartType('int', nullable: nullable);
      case 'number':
        return DartType('double', nullable: nullable);
      case 'boolean':
        return DartType('bool', nullable: nullable);
      case '__any':
        return const DartType('Object', nullable: true);
      case 'array':
        // MetricPoint is `[unix ms, value]`: a 2-tuple written with
        // prefixItems, which Dart has no type for, so it reads as the
        // List<double> both members fit -- a unix millisecond is well inside
        // what a double represents exactly.
        if (inner['items'] is YamlMap) {
          final item = _type(inner['items'] as YamlMap, '${name}Item');
          return DartType('List<${item.decl}>', nullable: nullable);
        }
        return null;
      case 'object':
        if (inner['additionalProperties'] is YamlMap) {
          final value = _type(
            inner['additionalProperties'] as YamlMap,
            '${name}Value',
          );
          return DartType('Map<String, ${value.decl}>', nullable: nullable);
        }
        // `additionalProperties: true` with no declared properties: an open bag
        // whose values the contract deliberately does not constrain, like an
        // incident event's `details`. A class would have no fields; the honest
        // Dart type is the map it is.
        if (inner['additionalProperties'] == true) {
          return DartType('Map<String, Object?>', nullable: nullable);
        }
        return null;
      default:
        return null;
    }
  }

  void _emitEnum(String name, List<String> values) {
    if (_enums.containsKey(name)) return;
    final members = <String, String>{};
    for (final v in values) {
      members[_memberName(v, members.keys.toSet())] = v;
    }
    final unknown = members.containsKey('unknown')
        ? 'unknownToThisBuild'
        : 'unknown';
    final b = StringBuffer()
      ..writeln('/// $name of the contract.')
      ..writeln('///')
      ..writeln(
        '/// `$unknown` is not in the contract: it is what a value this build has never',
      )
      ..writeln(
        '/// heard of becomes. A store build cannot be updated in step with the server it',
      )
      ..writeln(
        '/// talks to, so a value added there must leave this app readable rather than',
      )
      ..writeln('/// throwing on a screen that would otherwise have worked.')
      ..writeln('enum $name {');
    members.forEach((member, wire) => b.writeln("  $member('$wire'),"));
    b
      ..writeln('  $unknown(\'\');')
      ..writeln()
      ..writeln('  const $name(this.wire);')
      ..writeln()
      ..writeln('  /// The value as the API spells it; empty for $unknown.')
      ..writeln('  final String wire;')
      ..writeln()
      ..writeln('  static $name fromJson(Object? v, String path) {')
      ..writeln('    final s = _str(v, path);')
      ..writeln('    for (final e in values) {')
      ..writeln('      if (e.wire == s) return e;')
      ..writeln('    }')
      ..writeln('    return $unknown;')
      ..writeln('  }')
      ..writeln('}');
    _enums[name] = b.toString();
  }

  /// Flattens an `allOf` into one set of properties.
  ///
  /// Composition, not inheritance: ApmService is ApmRed plus its own fields,
  /// and the app wants one class with all of them rather than a wrapper around
  /// a base it would reach through on every screen. A branch that is a `$ref`
  /// is absorbed the same way, which is why ApmRed never becomes a class.
  void _absorb(YamlMap node, Map<String, Object?> props, Set<String> required) {
    final (inner, _) = _unwrapNullable(node);
    final ref = inner[r'$ref'];
    if (ref is String) {
      const prefix = '#/components/schemas/';
      if (!ref.startsWith(prefix)) {
        _fail('only component schema refs are supported, got $ref');
      }
      final target = _schemas[ref.substring(prefix.length)];
      if (target == null) _fail('allOf branch refers to unknown schema $ref');
      _absorb(target as YamlMap, props, required);
      return;
    }
    final all = inner['allOf'];
    if (all is YamlList) {
      for (final branch in all.cast<YamlMap>()) {
        _absorb(branch, props, required);
      }
    }
    final p = inner['properties'];
    if (p is YamlMap) {
      for (final e in p.entries) {
        props[e.key as String] = e.value;
      }
    }
    final r = inner['required'];
    if (r is YamlList) required.addAll(r.cast<String>());
  }

  void _emit(String name, YamlMap node) {
    if (_classes.containsKey(name)) return;
    // reserve the slot: a self-referencing schema must not recurse forever
    _classes[name] = '';
    final (unwrapped, _) = _unwrapNullable(node);
    final composed = unwrapped['allOf'] is YamlList;
    if (!composed &&
        unwrapped['type'] != 'object' &&
        unwrapped['properties'] == null) {
      _fail('$name is not an object; add it as an alias instead');
    }
    final merged = <String, Object?>{};
    final required = <String>{};
    _absorb(unwrapped, merged, required);
    final props = YamlMap.wrap(merged);

    final fields = <_Field>[];
    for (final entry in props.entries) {
      final key = entry.key as String;
      final t = _type(entry.value as YamlMap, '$name${_pascal(key)}');
      // Not required means it may be absent, which the app cannot tell from
      // null and must not have to.
      final nullable = t.nullable || !required.contains(key);
      fields.add(
        _Field(
          key,
          _fieldName(key),
          DartType(t.name, nullable: nullable),
          required.contains(key),
        ),
      );
    }

    if (fields.isEmpty) {
      // `const X({});` does not parse, and a schema with nothing in it is one
      // this generator has misread rather than one the app can use.
      _fail(
        '$name would be a class with no fields; it is probably a map or an '
        'alias this generator does not recognise yet',
      );
    }

    final desc = unwrapped['description'];
    final b = StringBuffer();
    if (desc is String) {
      for (final line in desc.trim().split('\n')) {
        b.writeln('/// ${line.trim()}');
      }
    } else {
      b.writeln('/// `$name` of the openlog API contract.');
    }
    b.writeln('class $name {');
    b.writeln('  const $name({');
    for (final f in fields) {
      b.writeln(
        f.type.nullable
            ? '    this.${f.dart},'
            : '    required this.${f.dart},',
      );
    }
    b.writeln('  });');
    b.writeln();
    b.writeln(
      '  factory $name.fromJson(Object? json, [String path = \'$name\']) {',
    );
    b.writeln('    final m = _obj(json, path);');
    b.writeln('    return $name(');
    for (final f in fields) {
      final reader = _reader(f.type.name);
      final fn = f.required && !f.type.nullable ? '_req' : '_opt';
      // `Object` is the one reader that returns a nullable of its own type
      // (_any is Object? Function(...)), so the type argument has to be
      // written out: inference takes it from the field and picks Object,
      // which _any does not fit. An inventory item's `data` is such a field.
      final targs = f.type.name == 'Object' ? '<Object?>' : '';
      b.writeln('      ${f.dart}: $fn$targs(m, \'${f.wire}\', path, $reader),');
    }
    b.writeln('    );');
    b.writeln('  }');
    b.writeln();
    // Writing it back out. Needed because several endpoints take the whole
    // object they gave: a routing rule has no enable endpoint, so turning one
    // off is a PUT of everything else unchanged, and hand-writing that for
    // each nested type is how a field gets dropped by omission.
    b.writeln('  Map<String, Object?> toJson() => {');
    for (final f in fields) {
      final value = _writer(f.type, f.dart);
      // A null that was absent should stay absent: the contract distinguishes
      // "not sent" from "sent as null" in several inputs, and sending null
      // where the server expected nothing is how a default gets cleared.
      //
      // A field that needs no conversion says that with Dart's null-aware map
      // entry; one that does keeps the `if`, because `?` cannot guard a
      // method call on the value.
      if (!f.type.nullable) {
        b.writeln("    '${f.wire}': $value,");
      } else if (value == '${f.dart}!') {
        b.writeln("    '${f.wire}': ?${f.dart},");
      } else {
        b.writeln("    if (${f.dart} != null) '${f.wire}': $value,");
      }
    }
    b.writeln('  };');
    b.writeln();
    for (final f in fields) {
      b.writeln('  final ${f.type.decl} ${f.dart};');
    }
    b.writeln('}');
    _classes[name] = b.toString();
  }

  /// The expression that turns one field into something `jsonEncode` accepts.
  ///
  /// `!` on a nullable field is safe because the caller only emits this inside
  /// an `if (field != null)`.
  String _writer(DartType type, String expr) {
    final bang = type.nullable ? '!' : '';
    final base = type.name;
    if (base.startsWith('(') && base.endsWith(')')) {
      final parts = _splitTypes(base.substring(1, base.length - 1));
      final written = <String>[];
      for (var i = 0; i < parts.length; i++) {
        final raw = parts[i];
        final optional = raw.endsWith('?');
        final element = DartType(
          optional ? raw.substring(0, raw.length - 1) : raw,
        );
        final access = '$expr$bang.\$${i + 1}';
        final value = _writer(element, access);
        written.add(
          optional && value != access
              ? '($access == null ? null : $value)'
              : value,
        );
      }
      return '[${written.join(', ')}]';
    }
    final list = RegExp(r'^List<(.+)>$').firstMatch(base);
    if (list != null) {
      final inner = DartType(
        list.group(1)!.replaceAll('?', ''),
        nullable: list.group(1)!.endsWith('?'),
      );
      // A nullable element stays null in the output; the list itself keeps
      // its length, because dropping a gap would shift every later point.
      final e = _writer(DartType(inner.name), 'e');
      final written = inner.nullable && e != 'e'
          ? '(e == null ? null : $e)'
          : e;
      // A list of things that need no conversion is already what jsonEncode
      // wants; rebuilding it element by element says nothing and reads worse.
      if (written == 'e') return '$expr$bang';
      return '[for (final e in $expr$bang) $written]';
    }
    final map = RegExp(r'^Map<String, (.+)>$').firstMatch(base);
    if (map != null) {
      final inner = DartType(
        map.group(1)!.replaceAll('?', ''),
        nullable: map.group(1)!.endsWith('?'),
      );
      if (inner.name == 'String' || inner.name == 'Object') {
        return '$expr$bang';
      }
      final v = _writer(inner, 'e.value');
      return '{for (final e in $expr$bang.entries) e.key: $v}';
    }
    switch (base) {
      case 'String':
      case 'bool':
      case 'int':
      case 'double':
      case 'Object':
        return '$expr$bang';
      case 'DateTime':
        return '$expr$bang.toUtc().toIso8601String()';
    }
    if (_enums.containsKey(base)) return '$expr$bang.wire';
    return '$expr$bang.toJson()';
  }

  /// The `(Object?, String) -> T` function that reads one value.
  String _reader(String type) {
    switch (type) {
      case 'String':
        return '_str';
      case 'bool':
        return '_bool';
      case 'int':
        return '_int';
      case 'double':
        return '_num';
      case 'DateTime':
        return '_time';
      case 'Object':
        return '_any';
    }
    // The element type keeps its `?`, the reader is chosen without it:
    // List<Object?> needs _list<Object?> so that _any, which returns Object?,
    // fits `T Function(Object?, String)`. Dropping the `?` here produced
    // _list<Object> against a nullable reader and did not compile.
    String base(String t) => t.endsWith('?') ? t.substring(0, t.length - 1) : t;

    // A nullable element is wrapped rather than read directly: Object? is
    // already null-tolerant, but `double?` would otherwise be read by _num,
    // which throws exactly where the contract says null belongs.
    String element(String t) {
      final b = base(t);
      final read = _reader(b);
      return t.endsWith('?') && b != 'Object' ? '_nullable<$b>($read)' : read;
    }

    if (type.startsWith('(') && type.endsWith(')')) {
      final parts = _splitTypes(type.substring(1, type.length - 1));
      final args = [for (final t in parts) element(t)].join(', ');
      final targs = [for (final t in parts) t].join(', ');
      return '(v, p) => _tuple${parts.length}<$targs>(v, p, $args)';
    }
    final list = RegExp(r'^List<(.+)>$').firstMatch(type);
    if (list != null) {
      final inner = list.group(1)!;
      return '(v, p) => _list<$inner>(v, p, ${element(inner)})';
    }
    final map = RegExp(r'^Map<String, (.+)>$').firstMatch(type);
    if (map != null) {
      final inner = map.group(1)!;
      return '(v, p) => _map<$inner>(v, p, ${element(inner)})';
    }
    if (_enums.containsKey(type)) return '$type.fromJson';
    return '(v, p) => $type.fromJson(v, p)';
  }

  /// Splits a record's parts on the commas between them, not on the commas
  /// inside a `Map<String, X>` within one.
  static List<String> _splitTypes(String body) {
    final out = <String>[];
    var depth = 0;
    var start = 0;
    for (var i = 0; i < body.length; i++) {
      final c = body[i];
      if (c == '<' || c == '(') depth++;
      if (c == '>' || c == ')') depth--;
      if (c == ',' && depth == 0) {
        out.add(body.substring(start, i).trim());
        start = i + 1;
      }
    }
    out.add(body.substring(start).trim());
    return out;
  }

  String _className(String context) => _pascal(context);
  String _enumName(String context) => _pascal(context);
}

class _Field {
  _Field(this.wire, this.dart, this.type, this.required);
  final String wire;
  final String dart;
  final DartType type;
  final bool required;
}

const _reserved = {
  'class',
  'const',
  'default',
  'enum',
  'extends',
  'final',
  'in',
  'is',
  'new',
  'null',
  'return',
  'super',
  'switch',
  'this',
  'true',
  'false',
  'var',
  'void',
  'while',
  'with',
  'for',
  'if',
  'else',
};

String _pascal(String s) => s
    .split(RegExp(r'[_\-. ]'))
    .where((p) => p.isNotEmpty)
    .map((p) => p[0].toUpperCase() + p.substring(1))
    .join();

String _camel(String s) {
  final p = _pascal(s);
  return p.isEmpty ? p : p[0].toLowerCase() + p.substring(1);
}

String _fieldName(String wire) {
  final n = _camel(wire);
  return _reserved.contains(n) ? '${n}Value' : n;
}

String _memberName(String wire, Set<String> taken) {
  var n = _camel(wire);
  if (n.isEmpty) n = 'empty';
  if (RegExp(r'^[0-9]').hasMatch(n)) n = 'v$n';
  if (_reserved.contains(n)) n = '${n}Value';
  while (taken.contains(n)) {
    n = '${n}_';
  }
  return n;
}
