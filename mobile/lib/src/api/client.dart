// The HTTP client for one openlog installation. Everything the app reads goes
// through here so that address handling, bearer tokens, the organization header
// and error mapping exist once.
import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

import 'schema.g.dart';

/// The address the app offers first. openlog is self-hosted, so this is a
/// pre-filled box and never a requirement: anyone running their own server
/// replaces it, and nothing else in the app knows this host is special -- no
/// certificate pinning, no code path of its own.
const defaultBaseUrl = 'https://apm.resoft.org';

/// A request that reached a server and came back as a failure.
///
/// Deliberately tolerant, because this is the one shape the contract cannot
/// promise: a 502 can come from a proxy as an HTML page and a captive portal
/// can answer anything at all. [code] and [message] are filled in when the body
/// is an openlog error and left to a plain description of the status when it is
/// not, so a screen always has something true to show.
class ApiException implements Exception {
  ApiException(
    this.status, {
    this.code = '',
    String? message,
    this.retryable = false,
  }) : message = message ?? 'the server answered $status';

  /// HTTP status, or 0 when the request never got an answer.
  final int status;

  /// openlog's error code (`unauthenticated`, `permission_denied`, …), or empty
  /// when the body was not an openlog error.
  final String code;
  final String message;

  /// The server said the same request is expected to succeed later.
  final bool retryable;

  bool get isUnauthenticated => status == 401;
  bool get isForbidden => status == 403;

  @override
  String toString() => message;
}

/// The address could not be reached at all: no DNS, no route, TLS refused,
/// or nothing answered in time. Separate from [ApiException] because the thing
/// to tell the person is different -- check the address, not your password.
class ApiUnreachable implements Exception {
  ApiUnreachable(this.baseUrl, this.cause);

  final String baseUrl;
  final Object cause;

  @override
  String toString() => 'cannot reach $baseUrl: $cause';
}

/// Turns what a person types into a base URL, or throws [FormatException].
///
/// People type `apm.resoft.org`, paste `https://apm.resoft.org/` and sometimes
/// paste a whole page URL. Guessing wrong here looks like the server being
/// down, which is the most expensive error on the first screen, so each rule is
/// deliberate rather than convenient.
String normalizeBaseUrl(String input) {
  var s = input.trim();
  if (s.isEmpty) {
    throw const FormatException('type the address of your openlog server');
  }
  // No scheme means https. Plain http stays http: a self-hosted install on a
  // private network is a real case, and silently upgrading it would fail in a
  // way that reads as "wrong address".
  if (!s.contains('://')) s = 'https://$s';
  final u = Uri.tryParse(s);
  if (u == null || u.host.isEmpty) {
    throw const FormatException('that does not look like an address');
  }
  if (u.scheme != 'https' && u.scheme != 'http') {
    throw FormatException(
      'the address must start with https:// or http://, not ${u.scheme}://',
    );
  }
  // A path is kept, because an install can live under one (/openlog), but the
  // query and fragment of a pasted page URL are not part of an address.
  var path = u.path;
  while (path.endsWith('/')) {
    path = path.substring(0, path.length - 1);
  }
  return Uri(
    scheme: u.scheme,
    host: u.host,
    port: u.hasPort ? u.port : null,
    path: path,
  ).toString();
}

/// One openlog installation, at one address, with at most one signed-in device
/// session.
class OpenlogClient {
  OpenlogClient({
    required String baseUrl,
    http.Client? httpClient,
    this.timeout = const Duration(seconds: 20),
  }) : baseUrl = normalizeBaseUrl(baseUrl),
       _http = httpClient ?? http.Client();

  final String baseUrl;
  final Duration timeout;
  final http.Client _http;

  /// The device session token (`olm_…`), or null while signed out.
  String? token;

  /// The organization to act in, for a person who belongs to several.
  String? orgId;

  void close() => _http.close();

  Uri _uri(String path) => Uri.parse('$baseUrl$path');

  Map<String, String> _headers({bool json = false}) {
    final h = <String, String>{'accept': 'application/json'};
    if (json) h['content-type'] = 'application/json';
    // No CSRF header: a device session is a bearer credential, and no other
    // site can make a request carry an Authorization header.
    final t = token;
    if (t != null) h['authorization'] = 'Bearer $t';
    final o = orgId;
    if (o != null) h['x-openlog-org-id'] = o;
    return h;
  }

  /// What this address allows, and whether it is an openlog server at all.
  ///
  /// The first call the app makes against a typed address: it needs no
  /// credentials, so it both proves the address and shapes the sign-in screen
  /// (is sign-up offered, how long must a password be, is there a CAPTCHA, is
  /// single sign-on available).
  Future<AuthConfig> authConfig() async =>
      AuthConfig.fromJson(await _send('GET', '/api/v1/auth/config'));

  /// Signs in and keeps the returned token for the calls that follow.
  ///
  /// [deviceName] is what the person will see in their session list on the web,
  /// and what they will pick out when signing one device out, so it is the
  /// device's own name rather than anything this app invents.
  Future<DeviceSession> signIn({
    required String email,
    required String password,
    required String deviceName,
  }) async {
    final body = await _send(
      'POST',
      '/api/v1/auth/device',
      body: {'email': email, 'password': password, 'device_name': deviceName},
    );
    final session = DeviceSession.fromJson(body);
    token = session.token;
    orgId = session.me.organization?.id;
    return session;
  }

  /// Creates an account and the organization it owns, then signs this device in.
  ///
  /// Two calls, because POST /api/v1/auth/signup answers the way the web needs
  /// it to -- a session cookie -- and a phone needs a bearer token. Signing in
  /// straight afterwards with the same credentials is the whole difference.
  ///
  /// `organization_name` is required by the contract, so this button creates an
  /// installation owner rather than a user: someone invited to an existing
  /// organization accepts that invitation on the web and then signs in here.
  Future<DeviceSession> signUp({
    required String email,
    required String password,
    required String name,
    required String organizationName,
    required String deviceName,
  }) async {
    await _send(
      'POST',
      '/api/v1/auth/signup',
      body: {
        'email': email,
        'password': password,
        'name': name,
        'organization_name': organizationName,
      },
    );
    return signIn(email: email, password: password, deviceName: deviceName);
  }

  /// Who the token belongs to, which organizations they are in and their role
  /// in the selected one. Read on every start, because a role change or a
  /// removed membership applies to the next request.
  Future<Me> me() async => Me.fromJson(await _send('GET', '/api/v1/auth/me'));

  /// What is firing, newest first.
  ///
  /// [states] is what the on-call screen asks for: `open` and `acknowledged`,
  /// never `resolved`, because a resolved incident is history and this is a
  /// screen about now. The counts come back for all of them regardless, which
  /// is how the screen can say "and 12 resolved today" without a second page.
  Future<IncidentPage> incidents({
    List<String> states = const ['open', 'acknowledged'],
    int limit = 50,
  }) async {
    final query = <String, String>{'limit': '$limit'};
    if (states.isNotEmpty) query['state'] = states.join(',');
    final path =
        '/api/v1/alerts/incidents?${Uri(queryParameters: query).query}';
    return IncidentPage.fromJson(await _send('GET', path));
  }

  /// Takes an incident, which stops its re-notifications.
  ///
  /// Idempotent on the server, and 409 when the incident has already resolved
  /// -- which is a race the screen should report rather than hide, since the
  /// person pressed a button about something that is no longer true.
  Future<void> acknowledgeIncident(String id) => _send(
    'POST',
    '/api/v1/alerts/incidents/${Uri.encodeComponent(id)}/acknowledge',
  );

  /// One incident with its timeline and what the server tried to deliver.
  ///
  /// The list row already says what is firing; this is what turns the row into
  /// a story -- when it opened, who took it, which channel failed and why.
  Future<AlertIncidentDetail> incident(String id) async =>
      AlertIncidentDetail.fromJson(
        await _send(
          'GET',
          '/api/v1/alerts/incidents/${Uri.encodeComponent(id)}',
        ),
      );

  /// Closes an incident, with an optional note on the timeline.
  ///
  /// 409 like [acknowledgeIncident]: someone else, or the server itself, got
  /// there first. A still-breaching series opens a new incident, so this is
  /// not a way to silence anything -- which is why the screen says so.
  Future<void> resolveIncident(String id, {String note = ''}) => _send(
    'POST',
    '/api/v1/alerts/incidents/${Uri.encodeComponent(id)}/resolve',
    body: note.isEmpty ? null : {'note': note},
  );

  /// Adds a note to the incident's timeline.
  Future<void> addIncidentNote(String id, String text) => _send(
    'POST',
    '/api/v1/alerts/incidents/${Uri.encodeComponent(id)}/notes',
    body: {'text': text},
  );

  /// Services with spans in the last hour, with their RED metrics.
  ///
  /// The range is left to the server's default (now - 1h), which is the window
  /// an on-call screen wants: what is happening, not what happened.
  Future<ServicePage> services({String q = ''}) async {
    final query = <String, String>{};
    if (q.isNotEmpty) query['q'] = q;
    final suffix = query.isEmpty ? '' : '?${Uri(queryParameters: query).query}';
    return ServicePage.fromJson(
      await _send('GET', '/api/v1/apm/services$suffix'),
    );
  }

  /// Golden signals of one service: totals plus the timeseries behind them.
  ///
  /// The window is the server's default, the same hour [services] uses, so a
  /// number on the list and a number on the detail screen cannot disagree.
  Future<ApmOverview> serviceOverview(String name) async =>
      ApmOverview.fromJson(
        await _send(
          'GET',
          '/api/v1/apm/services/${Uri.encodeComponent(name)}/overview',
        ),
      );

  /// What is actually breaking in that service: the error inbox, grouped.
  ///
  /// The window is the server's default again, and the workflow filters the
  /// web offers are left out on purpose -- a phone reads the inbox, it does
  /// not triage it.
  Future<ApmErrorInbox> serviceErrors(String name) async =>
      ApmErrorInbox.fromJson(
        await _send(
          'GET',
          '/api/v1/apm/services/${Uri.encodeComponent(name)}/errors',
        ),
      );

  /// One request end to end, every span of it, ordered by start time.
  Future<Trace> trace(String traceId) async => Trace.fromJson(
    await _send('GET', '/api/v1/traces/${Uri.encodeComponent(traceId)}'),
  );

  /// Entry spans, newest first, or the slowest ones.
  ///
  /// `root_only`, because a traces list is a list of requests: without it the
  /// first page would be a hundred database calls belonging to three requests,
  /// which is a span list and not what the person opened.
  Future<TracesQueryResponse> traces({
    String q = '',
    bool slowest = false,
    int limit = 50,
  }) async {
    final body = <String, Object?>{
      'root_only': true,
      'limit': limit,
      if (slowest) 'sort': 'duration',
    };
    if (q.trim().isNotEmpty) {
      // One case-insensitive contains over the service name: a phone has no
      // room for the web's filter builder, and the service is what a person
      // types when they are looking for a request.
      body['filters'] = [
        {'key': 'service_name', 'op': 'contains', 'value': q.trim()},
      ];
    }
    return TracesQueryResponse.fromJson(
      await _send('POST', '/api/v1/traces/query', body: body),
    );
  }

  /// Recent log records, newest first.
  Future<LogPage> logs({
    String q = '',
    String severityMin = '',
    int limit = 50,
  }) async {
    final query = <String, String>{'limit': '$limit'};
    if (q.isNotEmpty) query['q'] = q;
    if (severityMin.isNotEmpty) query['severity_min'] = severityMin;
    return LogPage.fromJson(
      await _send('GET', '/api/v1/logs?${Uri(queryParameters: query).query}'),
    );
  }

  /// Dashboards the signed-in person can see.
  Future<DashboardPageList> dashboards({String q = ''}) async {
    final query = <String, String>{};
    if (q.isNotEmpty) query['q'] = q;
    final suffix = query.isEmpty ? '' : '?${Uri(queryParameters: query).query}';
    return DashboardPageList.fromJson(
      await _send('GET', '/api/v1/dashboards$suffix'),
    );
  }

  /// One dashboard with its pages and widgets. The widgets carry queries, not
  /// data: each one is run separately with [runQuery].
  Future<Dashboard> dashboard(String id) async => Dashboard.fromJson(
    await _send('GET', '/api/v1/dashboards/${Uri.encodeComponent(id)}'),
  );

  /// Runs one OQL query, which is what a dashboard widget holds.
  Future<OqlResult> runQuery(String query) async => OqlResult.fromJson(
    await _send('POST', '/api/v1/query', body: {'query': query}),
  );

  // ---- the sections that are a list and nothing more ----
  //
  // Each is one GET with an optional search. They are grouped here rather than
  // spread out because that is all they are; anything that needs more than a
  // page and a query gets its own method above.

  Future<HostPage> hosts({String q = ''}) async =>
      HostPage.fromJson(await _send('GET', _listPath('/api/v1/hosts', q)));

  Future<ContainerPage> containers({String q = ''}) async =>
      ContainerPage.fromJson(
        await _send('GET', _listPath('/api/v1/containers', q)),
      );

  Future<PodPage> pods({String q = ''}) async => PodPage.fromJson(
    await _send('GET', _listPath('/api/v1/kubernetes/pods', q)),
  );

  /// `status=true` asks the server to compute each SLO's budget, which is the
  /// only reason to look at this list on a phone.
  Future<SloPage> slos({String q = ''}) async => SloPage.fromJson(
    await _send('GET', _listPath('/api/v1/slos', q, extra: {'status': 'true'})),
  );

  Future<SyntheticPage> synthetics({String q = ''}) async =>
      SyntheticPage.fromJson(
        await _send('GET', _listPath('/api/v1/synthetics/checks', q)),
      );

  Future<JobMonitorPage> jobMonitors({String q = ''}) async =>
      JobMonitorPage.fromJson(
        await _send('GET', _listPath('/api/v1/jobs/monitors', q)),
      );

  Future<VulnPage> vulnerabilities({String q = ''}) async => VulnPage.fromJson(
    await _send('GET', _listPath('/api/v1/vulnerabilities', q)),
  );

  Future<DbInstancePage> dbInstances({String q = ''}) async =>
      DbInstancePage.fromJson(
        await _send('GET', _listPath('/api/v1/db/instances', q)),
      );

  /// A path with `q` only when there is one: an empty `q=` is a filter that
  /// matches the empty string on some endpoints and everything on others, and
  /// neither is what an empty search box means.
  String _listPath(
    String path,
    String q, {
    Map<String, String> extra = const {},
  }) {
    final query = <String, String>{...extra};
    if (q.trim().isNotEmpty) query['q'] = q.trim();
    return query.isEmpty ? path : '$path?${Uri(queryParameters: query).query}';
  }

  /// Ends this device's session on the server and forgets the token here.
  ///
  /// The token is dropped even when the request fails: the person asked to be
  /// signed out, and leaving it in storage because the network was down would
  /// be the opposite of what they asked for. The session then expires on its
  /// own, and can be signed out from the web in the meantime.
  Future<void> signOut() async {
    try {
      await _send('POST', '/api/v1/auth/logout');
    } finally {
      token = null;
      orgId = null;
    }
  }

  Future<Object?> _send(
    String method,
    String path, {
    Map<String, Object?>? body,
  }) async {
    final request = http.Request(method, _uri(path))
      ..headers.addAll(_headers(json: body != null));
    if (body != null) request.body = jsonEncode(body);

    http.Response response;
    try {
      response = await http.Response.fromStream(
        await _http.send(request).timeout(timeout),
      );
    } on TimeoutException catch (e) {
      throw ApiUnreachable(baseUrl, 'no answer in ${timeout.inSeconds}s ($e)');
    } catch (e) {
      // SocketException, HandshakeException, ClientException: all of them mean
      // the same thing to the person reading the screen.
      throw ApiUnreachable(baseUrl, e);
    }

    if (response.statusCode >= 200 && response.statusCode < 300) {
      if (response.body.isEmpty) return null;
      try {
        return jsonDecode(response.body);
      } on FormatException catch (e) {
        // A 200 that is not JSON is almost always a captive portal or a proxy
        // answering for the server, so say that rather than "unexpected token".
        throw ApiException(
          response.statusCode,
          message:
              'that address answered with something other than JSON, '
              'so it is probably not an openlog server ($e)',
        );
      }
    }
    throw _error(response);
  }

  ApiException _error(http.Response response) {
    try {
      final body = jsonDecode(response.body);
      if (body is Map) {
        final err = body['error'];
        if (err is Map) {
          final message = err['message'];
          return ApiException(
            response.statusCode,
            code: err['code'] is String ? err['code'] as String : '',
            message: message is String && message.isNotEmpty ? message : null,
            retryable: err['retryable'] == true,
          );
        }
      }
    } catch (_) {
      // Not an openlog error body. The status alone is still worth reporting.
    }
    return ApiException(response.statusCode);
  }
}
