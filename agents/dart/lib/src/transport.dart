// Batching and delivery (docs/contracts/mobile-agent.md §5, §6).
//
// Sending is injectable so the tests never open a socket: what is worth testing here is when a batch goes
// out and what happens to it when the server says no, neither of which needs a network to be wrong.

import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'config.dart';
import 'limits.dart';
import 'otlp.dart';

/// Posts a body and answers with the HTTP status, or -1 when the request never completed.
typedef HttpSend = Future<int> Function(String url, Map<String, String> headers, String body);

/// The default sender. `dart:io` rather than `package:http`: this package has no dependencies, and an
/// application must never have to resolve a version of one because its telemetry SDK wanted it.
Future<int> defaultSend(String url, Map<String, String> headers, String body) async {
  final client = HttpClient();
  try {
    final req = await client.postUrl(Uri.parse(url));
    headers.forEach(req.headers.set);
    req.add(utf8.encode(body));
    final res = await req.close();
    await res.drain<void>();
    return res.statusCode;
  } on Object {
    return -1;
  } finally {
    client.close(force: true);
  }
}

class Transport {
  Transport(this.cfg, {HttpSend? send}) : _send = send ?? defaultSend;

  final ResolvedConfig cfg;
  final HttpSend _send;
  final List<Map<String, Object?>> _queue = [];
  Timer? _timer;

  /// Set when the server has refused the key itself. Nothing about the next attempt would differ, so the
  /// SDK stops rather than spending the application's battery on a permanent answer (§6).
  bool _refused = false;
  bool get refused => _refused;

  void add(Map<String, Object?> span) {
    if (_refused) return;
    if (_queue.length >= maxSpansPerRequest) {
      // Drop the oldest: the newest events are the ones still worth having, and an unbounded queue in a
      // long-lived application is a leak the application would be blamed for.
      _queue.removeAt(0);
    }
    _queue.add(span);
    if (_queue.length >= cfg.options.maxBatchSize) {
      unawaited(flush());
      return;
    }
    _schedule();
  }

  void _schedule() {
    if (_timer != null) return;
    _timer = Timer(cfg.options.flushInterval, () {
      _timer = null;
      unawaited(flush());
    });
  }

  Future<void> flush() async {
    _timer?.cancel();
    _timer = null;
    if (_refused || _queue.isEmpty) return;

    final batch = List<Map<String, Object?>>.of(_queue);
    _queue.clear();
    final body = jsonEncode(buildPayload(batch, _resource()));
    final status = await _send(cfg.rumUrl, {
      'content-type': 'application/json',
      'openlog-browser-key': cfg.options.key,
      'openlog-app-id': cfg.options.appId,
    }, body);

    if (status == 401 || status == 403) {
      _refused = true;
      _log('the server refused this key ($status); nothing further will be sent');
      return;
    }
    // 429 and 503 are temporary and say so; the batch goes back so the next flush carries it. Anything
    // else — including a request that never completed — is dropped rather than retried forever.
    if (status == 429 || status == 503 || status == -1) {
      _queue.insertAll(0, batch.take(maxSpansPerRequest - _queue.length));
      _schedule();
    }
  }

  Map<String, Object?> _resource() => {
        'service.version': cfg.options.serviceVersion,
        'device.model.identifier': cfg.options.deviceModel,
        'device.manufacturer': cfg.options.deviceManufacturer,
        'os.name': cfg.options.osName,
        'os.version': cfg.options.osVersion,
      };

  void _log(String message) {
    if (cfg.options.debug) stderr.writeln('[openlog] $message');
  }

  /// Sends what is queued and stops the timer. Called when the application goes to the background, which
  /// is the last reliable moment on a mobile platform.
  Future<void> stop() async {
    await flush();
    _timer?.cancel();
    _timer = null;
  }
}
