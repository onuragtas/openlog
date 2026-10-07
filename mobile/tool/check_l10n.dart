// Fails when the Turkish and English dictionaries disagree about what exists.
//
//   dart run tool/check_l10n.dart
//
// gen-l10n only warns about a missing translation and then falls back to
// English, which is exactly the failure that ships: a Turkish screen with two
// English words on it, noticed by a user rather than by CI. It also says nothing
// about placeholders, and `{min}` in one file against `{count}` in the other
// throws at runtime on the one screen that formats it.
import 'dart:convert';
import 'dart:io';

final _placeholder = RegExp(r'\{(\w+)\}');

void main() {
  final files =
      Directory('lib/l10n')
          .listSync()
          .whereType<File>()
          .where((f) => f.path.endsWith('.arb'))
          .toList()
        ..sort((a, b) => a.path.compareTo(b.path));

  const templateName = 'app_en.arb';
  final template = files.firstWhere(
    (f) => f.path.endsWith(templateName),
    orElse: () => throw StateError('lib/l10n/$templateName is missing'),
  );
  final base = _read(template);
  final problems = <String>[];

  for (final file in files) {
    if (file.path == template.path) continue;
    final other = _read(file);
    final name = file.uri.pathSegments.last;

    for (final key in base.keys) {
      if (!other.containsKey(key)) {
        problems.add(
          '$name: "$key" is missing, so that string would reach a '
          '${name.contains('_tr') ? 'Turkish' : name} screen in English',
        );
      }
    }
    for (final key in other.keys) {
      if (!base.containsKey(key)) {
        problems.add(
          '$name: "$key" is not in $templateName, so nothing shows it',
        );
      }
    }
    for (final key in base.keys) {
      if (!other.containsKey(key)) continue;
      final want = _placeholdersOf(base[key]!);
      final got = _placeholdersOf(other[key]!);
      if (!_sameSet(want, got)) {
        problems.add(
          '$name: "$key" uses {${got.join('}, {')}} where '
          '$templateName uses {${want.join('}, {')}} -- formatting it would throw',
        );
      }
    }
  }

  if (problems.isEmpty) {
    stdout.writeln(
      '${files.length} dictionaries agree on ${base.length} strings',
    );
    return;
  }
  for (final p in problems) {
    stderr.writeln('l10n: $p');
  }
  exit(1);
}

/// The translatable entries of one .arb: `@key` metadata and `@@locale` are not
/// strings anyone sees.
Map<String, String> _read(File f) {
  final json = jsonDecode(f.readAsStringSync());
  if (json is! Map) throw StateError('${f.path} is not a JSON object');
  return {
    for (final e in json.entries)
      if (!e.key.startsWith('@') && e.value is String)
        e.key as String: e.value as String,
  };
}

List<String> _placeholdersOf(String value) =>
    (_placeholder.allMatches(value).map((m) => m.group(1)!).toSet().toList())
      ..sort();

bool _sameSet(List<String> a, List<String> b) =>
    a.length == b.length &&
    List.generate(a.length, (i) => a[i] == b[i]).every((x) => x);
