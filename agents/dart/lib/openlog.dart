// openlog mobile SDK (docs/contracts/mobile-agent.md).
//
// Pure Dart: the SDK needs HTTP, timers and JSON, all of which are in the Dart SDK itself. A Flutter
// dependency would keep this out of Dart servers and CLIs for no gain and would put Flutter in CI to test
// code that does not use it. The one thing a Flutter application must wire by hand is the lifecycle — call
// onAppBackgrounded() when the app is paused — and that is three lines, shown in the README.
//
// What this does NOT do is as deliberate as what it does: it does not see a native crash of the engine or a plugin.
// That needs the Swift or Kotlin SDK in the host application (mobile-crash.md §2). recordFatal reports Dart errors.

import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'src/config.dart';
import 'src/ids.dart';
import 'src/limits.dart';
import 'src/otlp.dart';
import 'src/session.dart';
import 'src/transport.dart';
import 'src/visitor.dart';

export 'src/config.dart' show ConfigError, OpenlogOptions;

/// Reads `GET /v1/rum/config`, or null when it could not be read.
typedef ConfigFetch = Future<Map<String, Object?>?> Function(String url, Map<String, String> headers);

Future<Map<String, Object?>?> _defaultFetchConfig(String url, Map<String, String> headers) async {
  final client = HttpClient();
  try {
    final req = await client.getUrl(Uri.parse(url));
    headers.forEach(req.headers.set);
    final res = await req.close();
    if (res.statusCode != 200) {
      await res.drain<void>();
      return null;
    }
    final body = await res.transform(utf8.decoder).join();
    return jsonDecode(body) as Map<String, Object?>;
  } on Object {
    return null; // the SDK reports with its compiled-in settings; a page never depends on this to start
  } finally {
    client.close(force: true);
  }
}

/// The SDK. One per application: a second would double-count every screen.
class Openlog {
  Openlog._(this._session, this._transport, this._visitor);

  static Openlog? _active;

  final SessionState _session;
  final Transport _transport;
  final Visitor _visitor;
  String _userId = '';

  /// The screen last opened, which is where a fatal error happened.
  String _route = '';

  /// Starts the SDK. Calling it twice returns the first instance rather than starting a second.
  ///
  /// [send] and [fetchConfig] exist so tests never open a socket; applications leave them unset.
  static Openlog init(OpenlogOptions options, {HttpSend? send, ConfigFetch? fetchConfig}) {
    final existing = _active;
    if (existing != null) return existing;

    final cfg = resolveConfig(options); // throws rather than starting a silently useless SDK
    final visitor = Visitor(enabled: options.analyticsCollection, directory: options.visitorDirectory);
    final session = SessionState(options.sampleRate, roll: () => visitor.id.isEmpty ? nextUnit() : visitorRoll(visitor.id));
    final sdk = Openlog._(session, Transport(cfg, send: send), visitor);
    _active = sdk;

    // The operator's sample rate wins, so volume can be turned down without shipping a release. Until it
    // answers the application reports with its own, and failure is silent by design.
    // A mobile key refuses a request that names no application, and this one is no exception.
    unawaited((fetchConfig ?? _defaultFetchConfig)(
            cfg.configUrl, {'openlog-browser-key': options.key, 'openlog-app-id': options.appId})
        .then((conf) {
      if (conf == null) return;
      cfg.serviceName = (conf['service_name'] as String?) ?? '';
      cfg.environment = (conf['environment'] as String?) ?? '';
      final rate = conf['sample_rate'];
      if (rate is num) sdk._session.sampleRate = rate.toDouble();
      // Only this answer lets the id reach the disk. A server without the field is one without the feature.
      if (conf['analytics_mode'] == 'visitors') {
        visitor.confirm();
      } else {
        visitor.discard();
      }
    }));

    return sdk;
  }

  /// Drops the active instance (tests, and shutdown()).
  static void reset() => _active = null;

  /// The current session id, or '' when this session was not sampled.
  String get sessionId => _session.shouldSend ? _session.id : '';

  /// Attaches the application's own identifier for the person to every later span. Pass '' on sign-out.
  ///
  /// **Send an opaque, stable id — not an e-mail address or a name.** openlog stores it and never
  /// interprets it, so the discipline has to live here (mobile-agent.md §3.1).
  void identify(String userId) => _userId = truncate(userId.trim(), maxUserIdBytes);

  /// Turns recognising this person across launches on or off, e.g. from a consent dialog (analytics.md §1). Off deletes
  /// the stored id and is remembered across launches. It stops collection and erases nothing already sent: that is
  /// `POST /api/v1/analytics/erase`, called from your backend with [visitorId].
  void setAnalyticsCollection(bool enabled) => _visitor.setEnabled(enabled);

  /// The current visitor id, or '' when there is none.
  String get visitorId => _visitor.id;

  /// A screen the person opened. [isColdStart] marks the first one of a launch.
  void recordScreen(String name, {bool isColdStart = false, int durationMs = 0}) {
    final route = truncate(name.trim(), maxNameBytes);
    if (route.isEmpty) return;
    _route = route;
    if (!_session.touch()) _session.newScreen();
    _emit(RumEvent.screen, 'screen $route', durationMs: durationMs, spanId: _session.screenSpanId, attributes: {
      'openlog.rum.route': route,
      'openlog.rum.page_view.kind': isColdStart ? 'load' : 'route_change',
    });
  }

  /// An error the application caught itself.
  void recordError(Object error, {StackTrace? stack}) {
    final type = error.runtimeType.toString();
    _emit(RumEvent.error, 'error $type', isError: true, attributes: {
      'openlog.rum.error.source': 'error',
    }, exception: {
      'type': type,
      'message': error.toString(),
      'stacktrace': stack?.toString() ?? '',
    });
  }

  /// An uncaught Dart error, counted as a crash. Wire it to `FlutterError.onError` and `PlatformDispatcher.onError`.
  ///
  /// Sent at once rather than written to disk, because a Dart error usually leaves the process running.
  Future<void> recordFatal(Object error, {StackTrace? stack}) {
    final type = error.runtimeType.toString();
    _emit(RumEvent.error, 'error $type', isError: true, attributes: {
      'openlog.rum.route': _route,
      'openlog.rum.error.source': 'error',
      'openlog.rum.crash.kind': 'uncaught_exception',
      'openlog.rum.crash.id': newSessionId(),
    }, exception: {
      'type': type,
      'message': error.toString(),
      'stacktrace': stack?.toString() ?? '',
    });
    return _transport.flush();
  }

  /// An application-defined event, e.g. `recordEvent('checkout_started', params: {'plan': 'pro'})`.
  void recordEvent(String name, {Map<String, Object?>? params}) {
    final n = truncate(name.trim(), maxCustomNameBytes);
    if (n.isEmpty) return; // an unnamed event is an unqueryable row, and the server refuses it anyway
    _emit(RumEvent.custom, n, attributes: {'openlog.rum.custom.name': n, ..._customParams(params)});
  }

  /// An application-defined timing in milliseconds.
  void recordTiming(String name, int milliseconds, {Map<String, Object?>? params}) {
    final n = truncate(name.trim(), maxCustomNameBytes);
    if (n.isEmpty) return;
    _emit(RumEvent.custom, n, durationMs: milliseconds, attributes: {
      'openlog.rum.custom.name': n,
      'openlog.rum.custom.value': milliseconds,
      'openlog.rum.custom.unit': 'ms',
      ..._customParams(params),
    });
  }

  /// Parameters live in a namespace of their own so they can never collide with a field openlog later
  /// learns to interpret. Sorted before the cap is applied: which sixteen survive must depend on the
  /// payload, not on map order.
  Map<String, Object?> _customParams(Map<String, Object?>? params) {
    if (params == null || params.isEmpty) return const {};
    final keys = params.keys.toList()..sort();
    final out = <String, Object?>{};
    for (final k in keys) {
      if (out.length >= maxCustomParams) break;
      final key = k.trim();
      if (key.isEmpty || key.length > maxCustomParamKeyBytes) continue;
      if (!RegExp(r'^[a-z0-9_.-]+$').hasMatch(key)) continue;
      out['openlog.rum.custom.param.$key'] = params[k];
    }
    return out;
  }

  void _emit(
    String event,
    String name, {
    int durationMs = 0,
    String? spanId,
    Map<String, Object?> attributes = const {},
    bool isError = false,
    Map<String, String>? exception,
  }) {
    if (!_session.shouldSend) return; // a sampled-out session sends nothing at all
    _session.touch();
    _transport.add(buildSpan(
      name: name,
      event: event,
      traceId: _session.traceId,
      spanId: spanId ?? newSpanId(),
      // Everything hangs off the screen, so one trace holds the whole of it.
      parentSpanId: spanId == null ? _session.screenSpanId : null,
      startMs: DateTime.now().millisecondsSinceEpoch,
      durationMs: durationMs,
      isError: isError,
      exception: exception,
      attributes: {
        'session.id': _session.id,
        'openlog.rum.page_view.id': _session.screenSpanId,
        if (_userId.isNotEmpty) 'user.id': _userId,
        if (_visitor.id.isNotEmpty) 'openlog.rum.visitor.id': _visitor.id,
        ...attributes,
      },
    ));
  }

  /// Sends what is queued. Call this when the application goes to the background: on a mobile platform it
  /// is the last reliable moment, and the batch most likely to be lost is the final one.
  Future<void> onAppBackgrounded() => _transport.flush();

  Future<void> flush() => _transport.flush();

  Future<void> shutdown() async {
    await _transport.stop();
    if (identical(_active, this)) _active = null;
  }
}
