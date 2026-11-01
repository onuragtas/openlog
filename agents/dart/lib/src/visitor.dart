// The persistent visitor id behind DAU, MAU and retention (docs/contracts/analytics.md §1).
//
// Unlike a session this recognises someone across launches, so what it may do is narrower than what it looks like:
//
//  - It reaches the disk only once the server said this key is in `visitors` mode. Until then it lives in memory.
//  - A withdrawal outlives the process. Otherwise the next launch would hand someone who just said no a new id.
//  - Without a directory there is no id at all. A new one per launch would report every launch as a new person, which
//    is worse than reporting nothing: DAU and new users would look real and be wrong.

import 'dart:io';

import 'ids.dart';

const String _fileName = 'openlog_visitor';
const String _optOut = 'optout';

/// Rotated after 13 months, matching how long the server keeps an actor (analytics.md §1.2).
const Duration visitorMaxAge = Duration(days: 395);

/// Where this visitor falls in [0, 1): FNV-1a of the id over 2^32. Sampling is once per visitor when there is one
/// (analytics.md §2). Every SDK computes exactly this, so one person is in or out of the sample on all of them.
double visitorRoll(String id) {
  var h = 2166136261;
  for (final c in id.codeUnits) {
    h = ((h ^ c) * 16777619) & 0xffffffff;
  }
  return h / 4294967296;
}

class Visitor {
  Visitor({required bool enabled, String? directory, DateTime Function()? now})
      : _file = directory == null ? null : File('$directory${Platform.pathSeparator}$_fileName'),
        _now = now ?? DateTime.now {
    // An id on disk survives a default of false: an application that asks first starts every launch disabled and
    // enables after reading its own consent, and the person it asked must not become new each time.
    if (enabled && !_read().optOut) _adopt();
  }

  final File? _file;
  final DateTime Function() _now;
  String _id = '';
  int _createdAt = 0;
  bool _persistent = false;

  /// The id to send, or '' when there is none.
  String get id => _id;

  ({String id, int createdAt, bool optOut}) _read() {
    try {
      final lines = _file?.readAsLinesSync() ?? const <String>[];
      if (lines.isNotEmpty && lines.first == _optOut) return (id: '', createdAt: 0, optOut: true);
      if (lines.length >= 2) return (id: lines[0], createdAt: int.tryParse(lines[1]) ?? 0, optOut: false);
    } on Object {
      // A missing or unreadable file is no visitor, never an error in somebody's application.
    }
    return (id: '', createdAt: 0, optOut: false);
  }

  void _write(String? content) {
    final file = _file;
    if (file == null) return;
    try {
      if (content == null) {
        if (file.existsSync()) file.deleteSync();
        return;
      }
      // Renamed into place, so a crash mid-write cannot leave half an id behind.
      final tmp = File('${file.path}.tmp')..writeAsStringSync(content, flush: true);
      tmp.renameSync(file.path);
    } on Object {
      // Without storage the visitor is not remembered, and nothing else changes.
    }
  }

  void _adopt() {
    if (_file == null) return;
    final stored = _read();
    final nowMs = _now().millisecondsSinceEpoch;
    final fresh = RegExp(r'^[0-9a-f]{32}$').hasMatch(stored.id) && nowMs - stored.createdAt < visitorMaxAge.inMilliseconds;
    _id = fresh ? stored.id : newSessionId();
    _createdAt = fresh ? stored.createdAt : nowMs;
  }

  /// The server says this key recognises visitors.
  void confirm() {
    _persistent = true;
    if (_id.isNotEmpty) _write('$_id\n$_createdAt\n');
  }

  /// The server says this key does not: nothing stays on the device, except a person's own withdrawal.
  void discard() {
    _persistent = false;
    _id = '';
    if (!_read().optOut) _write(null);
  }

  /// Disabling deletes the id, so enabling again starts a new one.
  void setEnabled(bool enabled) {
    if (!enabled) {
      _id = '';
      _write('$_optOut\n');
      return;
    }
    if (_id.isNotEmpty) return;
    final wasOptOut = _read().optOut;
    _adopt();
    if (_persistent && _id.isNotEmpty) {
      _write('$_id\n$_createdAt\n');
    } else if (wasOptOut) {
      _write(null);
    }
  }
}
