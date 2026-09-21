// Options and their validation (docs/contracts/mobile-agent.md §1, §5).
//
// Everything that bounds an application — which applications a key serves, how much it may send, what share
// of sessions it keeps — lives on the key, not here. What is left is where to send and what this build calls
// itself, which only the application knows.

/// Thrown when init() is given options it cannot work with. It is thrown rather than logged: a telemetry
/// SDK that silently does nothing is worse than one that does not start, because nobody finds out for weeks.
class ConfigError implements Exception {
  ConfigError(this.message);
  final String message;
  @override
  String toString() => 'openlog: $message';
}

/// What an application passes to init().
class OpenlogOptions {
  OpenlogOptions({
    required this.key,
    required this.endpoint,
    required this.appId,
    this.serviceVersion = '',
    this.deviceModel = '',
    this.deviceManufacturer = '',
    this.osName = '',
    this.osVersion = '',
    this.sampleRate = 1,
    this.maxBatchSize = 32,
    this.flushInterval = const Duration(seconds: 5),
    this.debug = false,
  });

  /// The mobile key (`olb_…`). Public by construction: it ships inside the application binary, and the
  /// server bounds what it can do (mobile-agent.md §1).
  final String key;

  /// OTLP/HTTP base URL of openlog ingest, e.g. `https://ingest.example.com:4318`.
  final String endpoint;

  /// The Android package name or iOS bundle identifier, sent as `openlog-app-id`. Required: a mobile key is
  /// scoped by an application allowlist, and a request that declares nothing never matches one.
  final String appId;

  /// The build. `service.name` comes from the key and this does not — the name decides whose data this is,
  /// the version only labels a build within it.
  final String serviceVersion;
  final String deviceModel;
  final String deviceManufacturer;
  final String osName;
  final String osVersion;

  /// Share of sessions kept until `GET /v1/rum/config` answers with the operator's value.
  final double sampleRate;
  final int maxBatchSize;
  final Duration flushInterval;
  final bool debug;
}

/// Options after validation, with the URLs derived once.
class ResolvedConfig {
  ResolvedConfig._(this.options, this.rumUrl, this.configUrl);

  final OpenlogOptions options;
  final String rumUrl;
  final String configUrl;

  /// Set by `GET /v1/rum/config`; until it answers these are the compiled-in ones (mobile-agent.md §2).
  String serviceName = '';
  String environment = '';
}

/// Validates and derives. The checks are the server's own bounds, applied here so a mistake surfaces at
/// start-up in development rather than as a 400 in production.
ResolvedConfig resolveConfig(OpenlogOptions o) {
  if (!o.key.trim().startsWith('olb_')) {
    throw ConfigError('`key` must be a browser or mobile key (olb_…)');
  }
  final endpoint = o.endpoint.trim().replaceAll(RegExp(r'/+$'), '');
  if (!endpoint.startsWith('http://') && !endpoint.startsWith('https://')) {
    throw ConfigError('`endpoint` must be the http(s) URL of openlog ingest');
  }
  if (o.appId.trim().isEmpty) {
    throw ConfigError('`appId` is required: a mobile key is scoped by the applications it ships in');
  }
  if (!(o.sampleRate > 0 && o.sampleRate <= 1)) {
    throw ConfigError('`sampleRate` must be greater than 0 and at most 1');
  }
  if (o.maxBatchSize < 1 || o.maxBatchSize > 1000) {
    throw ConfigError('`maxBatchSize` must be between 1 and 1000');
  }
  if (o.flushInterval < const Duration(milliseconds: 500) || o.flushInterval > const Duration(seconds: 60)) {
    throw ConfigError('`flushInterval` must be between 500ms and 60s');
  }
  return ResolvedConfig._(o, '$endpoint/v1/rum', '$endpoint/v1/rum/config');
}
