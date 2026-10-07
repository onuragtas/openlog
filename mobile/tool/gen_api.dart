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
];

/// Responses that are declared inline on a path rather than as a named schema.
/// 'METHOD path status ClassName'.
const responseTargets = <String>['post /api/v1/auth/device 201 DeviceSession'];

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
    while (_pending.isNotEmpty) {
      final name = _pending.removeAt(0);
      if (!_done.add(name)) continue;
      final node = _schemas[name];
      if (node == null) _fail('schema $name is not in components.schemas');
      _emit(name, node as YamlMap);
    }
    // Inline response bodies last: they reference the named schemas above.
    responses.forEach(_emit);

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

  /// Strips a `oneOf` of exactly one non-null branch plus `type: "null"`, and a
  /// `type: [x, "null"]` union, returning the branch and that it is nullable.
  (YamlMap node, bool nullable) _unwrapNullable(YamlMap node) {
    final oneOf = node['oneOf'];
    if (oneOf is YamlList) {
      final branches = oneOf.cast<YamlMap>().toList();
      final nulls = branches.where((b) => b['type'] == 'null').toList();
      final rest = branches.where((b) => b['type'] != 'null').toList();
      if (nulls.length == 1 && rest.length == 1) return (rest.single, true);
      _fail('oneOf with ${branches.length} branches is not supported: $node');
    }
    final type = node['type'];
    if (type is YamlList) {
      final types = type.cast<String>().toList();
      final rest = types.where((t) => t != 'null').toList();
      if (types.contains('null') && rest.length == 1) {
        final copy = Map<String, Object?>.of(node.cast<String, Object?>())
          ..['type'] = rest.single;
        return (YamlMap.wrap(copy), true);
      }
      _fail('type union $types is not supported');
    }
    return (node, false);
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
      final aliased = _aliasOf(name, target as YamlMap);
      if (aliased != null) {
        return DartType(aliased.name, nullable: nullable || aliased.nullable);
      }
      // A referenced enum takes the schema's own name, so Role is `Role`
      // everywhere rather than OrgRefRole in one place and MeRole in another.
      final (inner, innerNullable) = _unwrapNullable(target);
      if (inner['type'] == 'string' && inner['enum'] != null) {
        _emitEnum(name, (inner['enum'] as YamlList).cast<String>().toList());
        return DartType(name, nullable: nullable || innerNullable);
      }
      _queue(name);
      return DartType(name, nullable: nullable);
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
      case 'array':
        final items = node['items'];
        if (items == null) _fail('array without items at $context');
        final inner = _type(items as YamlMap, '${context}Item');
        return DartType('List<${inner.decl}>', nullable: nullable);
      case 'object':
        final extra = node['additionalProperties'];
        if (node['properties'] == null && extra is YamlMap) {
          final inner = _type(extra, '${context}Value');
          return DartType('Map<String, ${inner.decl}>', nullable: nullable);
        }
        final name = _className(context);
        _emit(name, node);
        return DartType(name, nullable: nullable);
      default:
        _fail('unsupported type ${type ?? '(absent)'} at $context');
    }
  }

  /// Named schemas that are a scalar with no identity worth a class.
  DartType? _aliasOf(String name, YamlMap node) {
    final (inner, nullable) = _unwrapNullable(node);
    final ref = inner[r'$ref'];
    if (ref is String && nullable) {
      final t = _type(YamlMap.wrap({r'$ref': ref}), name);
      return DartType(t.name, nullable: true);
    }
    if (inner['type'] == 'string' &&
        inner['enum'] == null &&
        inner['properties'] == null) {
      if (inner['format'] == 'date-time') {
        return DartType('DateTime', nullable: nullable);
      }
      return DartType('String', nullable: nullable);
    }
    return null;
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

  void _emit(String name, YamlMap node) {
    if (_classes.containsKey(name)) return;
    _classes[name] =
        ''; // reserve the slot: a self-referencing schema must not recurse forever
    final (unwrapped, _) = _unwrapNullable(node);
    if (unwrapped['type'] != 'object' && unwrapped['properties'] == null) {
      _fail('$name is not an object; add it as an alias instead');
    }
    final props = (unwrapped['properties'] as YamlMap?) ?? YamlMap();
    final required = ((unwrapped['required'] as YamlList?) ?? YamlList())
        .cast<String>()
        .toSet();

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
      b.writeln('      ${f.dart}: $fn(m, \'${f.wire}\', path, $reader),');
    }
    b.writeln('    );');
    b.writeln('  }');
    b.writeln();
    for (final f in fields) {
      b.writeln('  final ${f.type.decl} ${f.dart};');
    }
    b.writeln('}');
    _classes[name] = b.toString();
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
    }
    final list = RegExp(r'^List<(.+?)\??>$').firstMatch(type);
    if (list != null) {
      final inner = list.group(1)!;
      return '(v, p) => _list<$inner>(v, p, ${_reader(inner)})';
    }
    final map = RegExp(r'^Map<String, (.+?)\??>$').firstMatch(type);
    if (map != null) {
      final inner = map.group(1)!;
      return '(v, p) => _map<$inner>(v, p, ${_reader(inner)})';
    }
    if (_enums.containsKey(type)) return '$type.fromJson';
    return '(v, p) => $type.fromJson(v, p)';
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
