// The HTTP client for one openlog installation. Everything the app reads goes
// through here so that address handling, bearer tokens, the organization header
// and error mapping exist once.
import 'dart:async';
import 'dart:convert';

import 'package:http/http.dart' as http;

import '../time_range.dart';
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

/// A template rendered into a rule, before anybody stores it.
///
/// [rule] is the server's own JSON, kept verbatim: it goes back to the server
/// twice, once to be previewed and once to be created, and anything this app
/// does not understand has to survive both trips.
class RenderedRule {
  const RenderedRule({
    required this.rule,
    required this.name,
    required this.type,
    this.severity,
    this.reference,
  });

  final Map<String, Object?> rule;
  final String name;
  final AlertRuleType type;
  final AlertSeverity? severity;

  /// Ratio templates: the metric the threshold was computed from, its latest
  /// value and the ratio. Shown because "80% of what" is the question.
  final AlertTemplateRenderReference? reference;
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

  /// The window every ranged request carries, or null while nothing has
  /// chosen one (tests, sign-in). Set by the app shell from the range
  /// picker.
  ///
  /// A function, not a value: a relative range has to be resolved when
  /// the request is made, or "the last hour" would freeze at the moment
  /// it was picked.
  TimeRange Function()? window;

  Uri _uri(String path) {
    final uri = Uri.parse('$baseUrl$path');
    final range = window?.call();
    // Only the paths the contract gives a `from`/`to`, and only when the
    // caller did not set its own window (the correlation screen asks
    // about an incident's window, not the screen's).
    if (range == null ||
        !pathTakesRange(uri.path) ||
        uri.queryParameters.containsKey('from') ||
        uri.queryParameters.containsKey('to')) {
      return uri;
    }
    return uri.replace(
      queryParameters: {...uri.queryParameters, ...range.query(DateTime.now())},
    );
  }

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

  /// The caller's own sessions: this phone, their browsers, their other
  /// devices.
  Future<SessionPage> sessions() async =>
      SessionPage.fromJson(await _send('GET', '/api/v1/sessions'));

  /// Ends one of them. The phone in a taxi is the case this exists for.
  Future<void> revokeSession(String id) =>
      _send('DELETE', '/api/v1/sessions/${Uri.encodeComponent(id)}');

  /// The routing rules, in evaluation order: where a page goes, and why it
  /// went where it did.
  Future<AlertRoutingRulePage> alertRoutingRules() async =>
      AlertRoutingRulePage.fromJson(
        await _send('GET', '/api/v1/alerts/routing-rules'),
      );

  /// Sets the evaluation order. Every rule of the organization has to be in
  /// the list exactly once -- the server checks, and a partial list is a 400
  /// rather than a quiet reshuffle.
  Future<void> reorderAlertRoutingRules(List<String> ids) =>
      _send('POST', '/api/v1/alerts/routing-rules/reorder', body: {'ids': ids});

  /// Turns one on or off.
  ///
  /// There is no enable endpoint, so this is a PUT of the whole rule; the
  /// other fields are sent back as they came to avoid editing them by
  /// omission.
  Future<void> setAlertRoutingRuleEnabled(
    AlertRoutingRule rule, {
    required bool enabled,
  }) => _send(
    'PUT',
    '/api/v1/alerts/routing-rules/${Uri.encodeComponent(rule.id)}',
    body: {
      'name': rule.name,
      'position': rule.position,
      'enabled': enabled,
      'is_default': rule.isDefault,
      'channel_ids': rule.channelIds,
      'match': rule.match.toJson(),
    },
  );

  /// The mute windows: what is silenced, and until when.
  Future<AlertMutePage> alertMutes() async =>
      AlertMutePage.fromJson(await _send('GET', '/api/v1/alerts/mutes'));

  /// Silences alerting until [endsAt], optionally only for [ruleIds].
  ///
  /// `starts_at` is sent explicitly rather than left out: the server would
  /// reject the body without it, and "now" has to be this device's idea of
  /// now, in UTC, so a phone in another timezone does not open a window in
  /// the past.
  Future<void> createAlertMute({
    required String name,
    required DateTime startsAt,
    required DateTime endsAt,
    List<String> ruleIds = const [],
    String comment = '',
  }) => _send(
    'POST',
    '/api/v1/alerts/mutes',
    body: {
      'name': name,
      'starts_at': startsAt.toUtc().toIso8601String(),
      'ends_at': endsAt.toUtc().toIso8601String(),
      if (comment.isNotEmpty) 'comment': comment,
      if (ruleIds.isNotEmpty) 'rule_ids': ruleIds,
    },
  );

  /// Ends one, by deleting it.
  Future<void> deleteAlertMute(String id) =>
      _send('DELETE', '/api/v1/alerts/mutes/${Uri.encodeComponent(id)}');

  /// The named date lists a recurring mute skips, ordered by name.
  Future<AlertHolidayCalendarPage> alertHolidayCalendars() async =>
      AlertHolidayCalendarPage.fromJson(
        await _send('GET', '/api/v1/alerts/holiday-calendars'),
      );

  /// Creates one.
  ///
  /// `description` is sent even when empty: the contract does not require it,
  /// but an edit that clears the description has to reach the server as an
  /// empty string rather than as "leave it alone".
  Future<void> createAlertHolidayCalendar({
    required String name,
    required String description,
    required List<String> dates,
  }) => _send(
    'POST',
    '/api/v1/alerts/holiday-calendars',
    body: {'name': name, 'description': description, 'dates': dates},
  );

  /// Replaces one. Mutes that use it pick up the new dates at their next check.
  Future<void> updateAlertHolidayCalendar(
    String id, {
    required String name,
    required String description,
    required List<String> dates,
  }) => _send(
    'PUT',
    '/api/v1/alerts/holiday-calendars/${Uri.encodeComponent(id)}',
    body: {'name': name, 'description': description, 'dates': dates},
  );

  /// Deletes one. The server answers 409 while a mute still references it.
  Future<void> deleteAlertHolidayCalendar(String id) => _send(
    'DELETE',
    '/api/v1/alerts/holiday-calendars/${Uri.encodeComponent(id)}',
  );

  /// The delivery log: notifications with their attempts, newest first.
  ///
  /// [channelId] and [status] are the server's own filters rather than a
  /// local one, because the limit is applied before any filtering -- asking
  /// for 200 and then keeping the failures would show the failures among the
  /// last 200 notifications, not the last 200 failures.
  Future<AlertDeliveryPage> alertDeliveries({
    String channelId = '',
    String status = '',
    int limit = 200,
  }) async => AlertDeliveryPage.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/alerts/deliveries',
        '',
        extra: {
          if (channelId.isNotEmpty) 'channel_id': channelId,
          if (status.isNotEmpty) 'status': status,
          'limit': '$limit',
        },
      ),
    ),
  );

  /// The notification channels, with their secrets masked by the server.
  Future<AlertChannelPage> alertChannels() async =>
      AlertChannelPage.fromJson(await _send('GET', '/api/v1/alerts/channels'));

  /// Sends a test notification through one, synchronously and without
  /// retries.
  ///
  /// A 200 does not mean it worked: the body carries `success`, and a channel
  /// whose receiver refused answers 200 with success false. Treating the
  /// status code as the answer would tell someone their pager works when it
  /// does not.
  Future<AlertChannelTestResult> testAlertChannel(String id) async =>
      AlertChannelTestResult.fromJson(
        await _send(
          'POST',
          '/api/v1/alerts/channels/${Uri.encodeComponent(id)}/test',
        ),
      );

  /// The recommended templates, which is how a rule gets made on a phone.
  ///
  /// Making one from scratch means choosing a metric, an aggregation, a
  /// window and two thresholds; a template is the same rule with the choices
  /// already made by someone who knew the metric.
  Future<AlertTemplatePage> alertTemplates({String category = ''}) async =>
      AlertTemplatePage.fromJson(
        await _send(
          'GET',
          _listPath(
            '/api/v1/alerts/templates',
            '',
            extra: {if (category.isNotEmpty) 'category': category},
          ),
        ),
      );

  /// Which rule types this installation can use, and why not when it cannot.
  Future<AlertRuleTypePage> alertRuleTypes() async =>
      AlertRuleTypePage.fromJson(
        await _send('GET', '/api/v1/alerts/rule-types'),
      );

  /// Turns a template and its values into a rule nobody has stored yet.
  ///
  /// [language] is the language of the generated name and description, so a
  /// rule made from a Turkish phone reads as Turkish on the web too.
  Future<RenderedRule> renderAlertTemplate(
    String id, {
    required Map<String, Object?> params,
    required String language,
    List<String> channelIds = const [],
  }) async {
    final body = await _send(
      'POST',
      '/api/v1/alerts/templates/${Uri.encodeComponent(id)}/render',
      body: {
        'params': params,
        'language': language,
        if (channelIds.isNotEmpty) 'channel_ids': channelIds,
      },
    );
    final parsed = AlertTemplateRender.fromJson(body);
    // The rule is kept exactly as it arrived and sent on untouched. Parsing
    // it into the typed input and writing that back would quietly drop any
    // condition field this app's copy of the contract does not know about --
    // and a rule with a field missing is a different rule.
    final raw = (body as Map<String, Object?>)['rule'];
    return RenderedRule(
      rule: raw is Map<String, Object?> ? raw : const {},
      reference: parsed.reference,
      name: parsed.rule.name,
      severity: parsed.rule.severity,
      type: parsed.rule.type,
    );
  }

  /// What a rule would have done over the last [hours] hours, without storing
  /// it. Viewer is enough: this reads telemetry, it does not change anything.
  Future<AlertRulePreview> previewAlertRule(
    Map<String, Object?> rule, {
    int hours = 6,
  }) async => AlertRulePreview.fromJson(
    await _send(
      'POST',
      '/api/v1/alerts/rules/preview',
      body: {'rule': rule, 'hours': hours},
    ),
  );

  /// Stores it. The body is the rendered rule as the server wrote it.
  Future<void> createAlertRule(Map<String, Object?> rule) =>
      _send('POST', '/api/v1/alerts/rules', body: rule);

  /// The rules behind the incidents, ordered by name.
  Future<AlertRulePage> alertRules() async =>
      AlertRulePage.fromJson(await _send('GET', '/api/v1/alerts/rules'));

  /// Turns a rule on, or off.
  ///
  /// Disabling resolves the rule's open incidents with reason `rule_disabled`
  /// -- the server says so, and it is not what "stop paging me" sounds like,
  /// so the screen has to say it before the tap, not after.
  Future<void> setAlertRuleEnabled(String id, {required bool enabled}) => _send(
    'POST',
    '/api/v1/alerts/rules/${Uri.encodeComponent(id)}/'
        '${enabled ? 'enable' : 'disable'}',
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

  /// Where the service runs: its instances, and the hosts that reported it.
  Future<ApmServiceDetail> apmService(String service) async =>
      ApmServiceDetail.fromJson(
        await _send(
          'GET',
          '/api/v1/apm/services/${Uri.encodeComponent(service)}',
        ),
      );

  /// Its Apdex threshold, and whether it is the organization's default.
  Future<ApmSettings> apmServiceSettings(String service) async =>
      ApmSettings.fromJson(
        await _send(
          'GET',
          '/api/v1/apm/services/${Uri.encodeComponent(service)}/settings',
        ),
      );

  /// Sets it. Admin or owner; 404 where there is nothing to store settings
  /// in (static auth mode).
  Future<ApmSettings> putApmServiceSettings(
    String service, {
    required int apdexTMs,
  }) async => ApmSettings.fromJson(
    await _send(
      'PUT',
      '/api/v1/apm/services/${Uri.encodeComponent(service)}/settings',
      body: {'apdex_t_ms': apdexTMs},
    ),
  );

  /// The containers the service's spans came from.
  Future<ApmServiceContainerPage> apmServiceContainers(String service) async =>
      ApmServiceContainerPage.fromJson(
        await _send(
          'GET',
          '/api/v1/apm/services/${Uri.encodeComponent(service)}/containers',
        ),
      );

  /// And the pods those containers belong to.
  Future<ApmServicePodPage> apmServicePods(String service) async =>
      ApmServicePodPage.fromJson(
        await _send(
          'GET',
          '/api/v1/apm/services/${Uri.encodeComponent(service)}/kubernetes',
        ),
      );

  /// When each version of a service first appeared.
  Future<ApmDeploymentPage> apmDeployments({required String service}) async =>
      ApmDeploymentPage.fromJson(
        await _send(
          'GET',
          '/api/v1/apm/services/${Uri.encodeComponent(service)}/deployments',
        ),
      );

  /// What one deployment did: the half hour before it against the half hour
  /// after, and the error groups first seen since.
  ///
  /// [at] is sent as unix milliseconds, which the server truncates to the
  /// minute -- the same value its own deployment list carries, so the
  /// comparison is of the deployment that was tapped and not of a timestamp
  /// this app re-formatted.
  Future<ApmDeploymentCompare> apmDeploymentCompare({
    required String service,
    required int atMillis,
    String window = '30m',
  }) async => ApmDeploymentCompare.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/apm/services/${Uri.encodeComponent(service)}'
            '/deployments/compare',
        '',
        extra: {'at': '$atMillis', 'window': window},
      ),
    ),
  );

  /// The transactions of one service: what it spends its time on.
  ///
  /// `time` is the server's default sort and means "by time consumed",
  /// which is the one that answers "where does the service's time go".
  Future<ApmTransactionPage> apmTransactions({
    required String service,
    String sort = 'time',
  }) async => ApmTransactionPage.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/apm/services/${Uri.encodeComponent(service)}/transactions',
        '',
        extra: {'sort': sort},
      ),
    ),
  );

  /// The database statements one service runs, normalized by the server.
  Future<ApmDbQueryPage> apmServiceDatabases({
    required String service,
    String sort = 'time',
  }) async => ApmDbQueryPage.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/apm/services/${Uri.encodeComponent(service)}/databases',
        '',
        extra: {'sort': sort},
      ),
    ),
  );

  /// What calls what.
  ///
  /// With [service], only the edges touching it -- which is the only shape
  /// worth asking for on a phone: a whole map is a picture, and a picture of
  /// forty services does not fit.
  Future<ApmMap> apmMap({String service = ''}) async => ApmMap.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/apm/map',
        '',
        extra: {if (service.isNotEmpty) 'service': service},
      ),
    ),
  );

  /// Which nodes and edges of that map one transaction's traces use.
  Future<ApmMapPath> apmMapPath({
    required String service,
    required String transaction,
  }) async => ApmMapPath.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/apm/map/path',
        '',
        extra: {'service': service, 'transaction': transaction},
      ),
    ),
  );

  /// Entry spans of one service, with the web's filters.
  ///
  /// `attr.<key>=<value>` pairs are passed through as the server names them;
  /// at most ten, which is the server's limit too.
  Future<ApmTracePage> apmTraces({
    required String service,
    String transaction = '',
    String minDurationMs = '',
    String maxDurationMs = '',
    bool errorsOnly = false,
    String sort = 'timestamp',
    Map<String, String> attributes = const {},
    int limit = 50,
  }) async => ApmTracePage.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/apm/traces',
        '',
        extra: {
          'service': service,
          if (transaction.isNotEmpty) 'transaction': transaction,
          if (minDurationMs.isNotEmpty) 'min_duration_ms': minDurationMs,
          if (maxDurationMs.isNotEmpty) 'max_duration_ms': maxDurationMs,
          // Only when it is on: `error=false` asks for the traces that did
          // not fail, which is not what an unchecked box means.
          if (errorsOnly) 'error': 'true',
          'sort': sort,
          'limit': '$limit',
          for (final e in attributes.entries) 'attr.${e.key}': e.value,
        },
      ),
    ),
  );

  /// Which traces are kept and which are thrown away.
  Future<TailSamplingPolicyState> tailSampling() async =>
      TailSamplingPolicyState.fromJson(
        await _send('GET', '/api/v1/apm/sampling'),
      );

  /// Stores a policy.
  ///
  /// [version] is the version that was edited (0 when nothing is stored);
  /// the server answers 409 when somebody else saved in the meantime, which
  /// is the point of sending it.
  Future<TailSamplingPolicyState> putTailSampling(
    TailSamplingPolicy policy, {
    required int version,
  }) async => TailSamplingPolicyState.fromJson(
    await _send(
      'PUT',
      '/api/v1/apm/sampling',
      body: {'policy': policy.toJson(), 'version': version},
    ),
  );

  /// What a policy nobody saved yet would keep, from the traces already
  /// stored. Rate limits are not simulated, so `max_spans_per_second` does
  /// not show up in the answer.
  Future<TailSamplingPreview> previewTailSampling(
    TailSamplingPolicy policy, {
    int windowMinutes = 60,
  }) async => TailSamplingPreview.fromJson(
    await _send(
      'POST',
      '/api/v1/apm/sampling/preview',
      body: {'policy': policy.toJson(), 'window_minutes': windowMinutes},
    ),
  );

  /// Which language agent each service runs, and how far behind it is.
  ///
  /// `upgrade=false`: the upgrade commands make the server check package
  /// registries, and a phone shows versions rather than running the upgrade.
  Future<ApmAgentsResponse> apmAgents() async => ApmAgentsResponse.fromJson(
    await _send(
      'GET',
      _listPath('/api/v1/apm/agents', '', extra: {'upgrade': 'false'}),
    ),
  );

  /// One error group, with everything the web's panel shows: the stack
  /// trace, the sample requests it happened in, who it hit, the comments and
  /// the history.
  Future<ApmErrorGroupDetail> apmErrorGroup({
    required String service,
    required String groupId,
  }) async => ApmErrorGroupDetail.fromJson(
    await _send(
      'GET',
      '/api/v1/apm/services/${Uri.encodeComponent(service)}'
          '/errors/${Uri.encodeComponent(groupId)}',
    ),
  );

  /// Which services a host runs.
  Future<ApmHostServicePage> apmHostServices(String hostId) async =>
      ApmHostServicePage.fromJson(
        await _send(
          'GET',
          '/api/v1/apm/hosts/${Uri.encodeComponent(hostId)}/services',
        ),
      );

  /// The error inbox of every service.
  ///
  /// The filters the web offers are all here now: a phone that could only
  /// read the inbox left the person with a list they could not act on, which
  /// is the one thing an inbox is for.
  Future<ApmErrorInbox> apmErrors({
    String service = '',
    String status = 'unresolved',
    String assignee = '',
    String q = '',
    String sort = 'count',
    int limit = 50,
  }) async => ApmErrorInbox.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/apm/errors',
        '',
        extra: {
          if (service.isNotEmpty) 'service': service,
          // `all` is this app's word for "no status filter"; the server
          // takes the parameter away, not a fourth value.
          if (status.isNotEmpty && status != 'all') 'status': status,
          if (assignee.isNotEmpty) 'assignee': assignee,
          if (q.isNotEmpty) 'q': q,
          'sort': sort,
          'limit': '$limit',
        },
      ),
    ),
  );

  /// Resolves, ignores, reopens or assigns groups.
  ///
  /// One call for several groups because that is the endpoint: the server
  /// writes one audit event per changed group either way.
  Future<void> patchApmErrorGroups(
    List<String> groupIds, {
    String? status,
    String? assigneeUserId,
    String? resolvedInVersion,
  }) => _send(
    'PATCH',
    '/api/v1/apm/errors/groups',
    body: {
      'group_ids': groupIds,
      // Omitted means unchanged, and an empty assignee means unassign --
      // two different things, so null and '' cannot be merged here.
      'status': ?status,
      'assignee_user_id': ?assigneeUserId,
      'resolved_in_version': ?resolvedInVersion,
    },
  );

  /// What people said about one group, oldest first.
  Future<ApmErrorCommentPage> apmErrorComments(String groupId) async =>
      ApmErrorCommentPage.fromJson(
        await _send(
          'GET',
          '/api/v1/apm/errors/groups/${Uri.encodeComponent(groupId)}/comments',
        ),
      );

  Future<void> addApmErrorComment(String groupId, String body) => _send(
    'POST',
    '/api/v1/apm/errors/groups/${Uri.encodeComponent(groupId)}/comments',
    body: {'body': body},
  );

  /// The author deletes their own; an admin or owner deletes any.
  Future<void> deleteApmErrorComment(String groupId, String commentId) => _send(
    'DELETE',
    '/api/v1/apm/errors/groups/${Uri.encodeComponent(groupId)}'
        '/comments/${Uri.encodeComponent(commentId)}',
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
  /// One host.
  Future<Host> host(String hostId) async => Host.fromJson(
    await _send('GET', '/api/v1/hosts/${Uri.encodeComponent(hostId)}'),
  );

  /// What the agent found running on it: the `discovered_service` items of the
  /// host's latest snapshot, integration status and all.
  Future<InventoryResponse> hostServices(String hostId) async =>
      InventoryResponse.fromJson(
        await _send(
          'GET',
          '/api/v1/hosts/${Uri.encodeComponent(hostId)}/services',
        ),
      );

  /// One container, with the attributes the list leaves out.
  Future<ContainerDetail> container(String id) async =>
      ContainerDetail.fromJson(
        await _send('GET', '/api/v1/containers/${Uri.encodeComponent(id)}'),
      );

  /// And what it has been doing.
  Future<ContainerTimeseries> containerTimeseries(String id) async =>
      ContainerTimeseries.fromJson(
        await _send(
          'GET',
          '/api/v1/containers/${Uri.encodeComponent(id)}/timeseries',
        ),
      );

  /// One pod, with its containers, labels and the services running in it.
  Future<KubernetesPodDetail> pod(String podUid) async =>
      KubernetesPodDetail.fromJson(
        await _send(
          'GET',
          '/api/v1/kubernetes/pods/${Uri.encodeComponent(podUid)}',
        ),
      );

  Future<KubernetesPodTimeseries> podTimeseries(String podUid) async =>
      KubernetesPodTimeseries.fromJson(
        await _send(
          'GET',
          '/api/v1/kubernetes/pods/${Uri.encodeComponent(podUid)}/timeseries',
        ),
      );

  /// The events about it, newest first. On a pod that will not start this is
  /// the answer -- BackOff, FailedScheduling, OOMKilled -- and nothing else
  /// on the screen says it.
  Future<KubernetesEventList> podEvents(
    String podUid, {
    int limit = 50,
  }) async => KubernetesEventList.fromJson(
    await _send(
      'GET',
      '/api/v1/kubernetes/pods/${Uri.encodeComponent(podUid)}/events?limit=$limit',
    ),
  );

  /// What an SDK or an agent has to be pointed at: the OTLP endpoints, the
  /// release the install commands pin, and what this installation supports.
  ///
  /// Returns no secrets -- the contract says so -- so there is no license key
  /// on this screen and none to keep off it.
  Future<Onboarding> onboarding() async =>
      Onboarding.fromJson(await _send('GET', '/api/v1/onboarding'));

  /// What has been profiled: one entry per service, environment and profile
  /// type. The type is never guessed -- nanoseconds and bytes do not add up,
  /// so the other endpoints take the type this list reports.
  Future<ProfileServicePage> profileServices() async =>
      ProfileServicePage.fromJson(
        await _send('GET', '/api/v1/profiles/services'),
      );

  /// Functions ranked by self time. `total` is the sum of the rows returned,
  /// not of the window, so a share is of something the person can see.
  Future<ProfileFunctionPage> profileFunctions({
    required String service,
    required String type,
    String environment = '',
    int limit = 50,
  }) async {
    final query = <String, String>{
      'service': service,
      'type': type,
      'limit': '$limit',
    };
    if (environment.isNotEmpty) query['environment'] = environment;
    return ProfileFunctionPage.fromJson(
      await _send(
        'GET',
        '/api/v1/profiles/functions?${Uri(queryParameters: query).query}',
      ),
    );
  }

  /// The flame graph of one profile: a tree of frames, each with the total
  /// below it.
  ///
  /// Identical stacks are folded by the server, which is why this is one
  /// request and not a page of them.
  Future<ProfileFlame> profileFlame({
    required String service,
    required String type,
    String environment = '',
  }) async {
    final query = <String, String>{'service': service, 'type': type};
    if (environment.isNotEmpty) query['environment'] = environment;
    return ProfileFlame.fromJson(
      await _send(
        'GET',
        '/api/v1/profiles/flame?${Uri(queryParameters: query).query}',
      ),
    );
  }

  /// How far behind the agents are, fleet-wide.
  Future<FleetSummary> fleetSummary() async =>
      FleetSummary.fromJson(await _send('GET', '/api/v1/fleet/summary'));

  /// The integration settings that apply to one host, in the order they
  /// are applied, with the revisions the host has and has applied.
  Future<IntegrationSettingsList> integrationSettings({
    required String hostId,
  }) async => IntegrationSettingsList.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/integrations/settings',
        '',
        extra: {'host_id': hostId},
      ),
    ),
  );

  /// Stores a new setting.
  Future<IntegrationSetting> createIntegrationSetting(
    Map<String, Object?> body,
  ) async => IntegrationSetting.fromJson(
    await _send('POST', '/api/v1/integrations/settings', body: body),
  );

  /// Replaces one. Every non-secret field is replaced, so the whole
  /// setting goes back; a password left out keeps the stored one and an
  /// empty one clears it, as the contract says.
  Future<IntegrationSetting> updateIntegrationSetting(
    String id,
    Map<String, Object?> body,
  ) async => IntegrationSetting.fromJson(
    await _send(
      'PUT',
      '/api/v1/integrations/settings/${Uri.encodeComponent(id)}',
      body: body,
    ),
  );

  /// How the fleet updates itself: the mode, the channel, the waves and
  /// the windows.
  Future<FleetPolicy> fleetPolicy() async =>
      FleetPolicy.fromJson(await _send('GET', '/api/v1/fleet/policy'));

  /// Stores the policy. The whole policy goes back, as the contract takes
  /// it, so a screen that changes one field sends the rest unchanged
  /// rather than emptying it.
  Future<FleetPolicy> putFleetPolicy(Map<String, Object?> policy) async =>
      FleetPolicy.fromJson(
        await _send('PUT', '/api/v1/fleet/policy', body: policy),
      );

  /// The rollouts, newest first.
  Future<FleetRolloutPage> fleetRollouts({int limit = 20}) async =>
      FleetRolloutPage.fromJson(
        await _send('GET', '/api/v1/fleet/rollouts?limit=$limit'),
      );

  /// Stops a rollout where it is. The agents already updated stay as they
  /// are; the waves that have not started do not.
  Future<FleetRollout> pauseRollout(String id) async => FleetRollout.fromJson(
    await _send(
      'POST',
      '/api/v1/fleet/rollouts/${Uri.encodeComponent(id)}/pause',
    ),
  );

  /// Starts a paused or halted rollout again, which restarts its soak.
  Future<FleetRollout> resumeRollout(String id) async => FleetRollout.fromJson(
    await _send(
      'POST',
      '/api/v1/fleet/rollouts/${Uri.encodeComponent(id)}/resume',
    ),
  );

  /// Jumps a rollout to its last wave: the rest of the fleet, now.
  Future<FleetRollout> deployRolloutNow(String id) async =>
      FleetRollout.fromJson(
        await _send(
          'POST',
          '/api/v1/fleet/rollouts/${Uri.encodeComponent(id)}/deploy-now',
        ),
      );

  /// Rolls the fleet back to a version it was running.
  Future<FleetRollout> rollbackFleet(String toVersion) async =>
      FleetRollout.fromJson(
        await _send(
          'POST',
          '/api/v1/fleet/rollback',
          body: {'to_version': toVersion},
        ),
      );

  /// The agents themselves, ordered by host name.
  Future<FleetHostPage> fleetHosts({String q = '', int limit = 100}) async {
    final query = <String, String>{'limit': '$limit'};
    if (q.trim().isNotEmpty) query['q'] = q.trim();
    return FleetHostPage.fromJson(
      await _send(
        'GET',
        '/api/v1/fleet/hosts?${Uri(queryParameters: query).query}',
      ),
    );
  }

  /// Inventory items of one category whose key contains [q], across every
  /// host's latest snapshot.
  ///
  /// The category is required by the server and there is no "all": the
  /// categories hold different things and a mixed list would be unreadable.
  Future<InventoryPage> inventory({
    required String category,
    String q = '',
    int limit = 100,
  }) async {
    final query = <String, String>{'category': category, 'limit': '$limit'};
    if (q.trim().isNotEmpty) query['q'] = q.trim();
    return InventoryPage.fromJson(
      await _send(
        'GET',
        '/api/v1/inventory/search?${Uri(queryParameters: query).query}',
      ),
    );
  }

  /// Hosts by cost, most expensive first, with the fleet summary and the
  /// provenance of the prices in the same answer.
  Future<CostHostPage> costHosts({int limit = 50}) async =>
      CostHostPage.fromJson(
        await _send('GET', '/api/v1/costs/hosts?limit=$limit'),
      );

  /// What each service costs: the containers linked to it, on the hosts
  /// that ran them.
  Future<CostServicePage> costServices({int limit = 50}) async =>
      CostServicePage.fromJson(
        await _send('GET', '/api/v1/costs/services?limit=$limit'),
      );

  /// The fleet's cost over time, with the idle part of it.
  Future<CostTrend> costTrend() async =>
      CostTrend.fromJson(await _send('GET', '/api/v1/costs/trend'));

  /// Browser applications that reported in the range.
  Future<RumAppPage> rumApps() async =>
      RumAppPage.fromJson(await _send('GET', '/api/v1/rum/apps'));

  /// One application's Core Web Vitals, page views and totals.
  Future<RumOverview> rumOverview(String app) async => RumOverview.fromJson(
    await _send(
      'GET',
      '/api/v1/rum/overview?${Uri(queryParameters: {'app': app}).query}',
    ),
  );

  /// Metric names with data points in the range.
  ///
  /// `q` matches the name or any service that sent it, so typing a service
  /// lists its metrics -- which is how a person who knows the service but not
  /// the metric name finds anything here.
  Future<MetricListResponse> metrics({String q = '', int limit = 200}) async {
    final query = <String, String>{'limit': '$limit'};
    if (q.trim().isNotEmpty) query['q'] = q.trim();
    return MetricListResponse.fromJson(
      await _send(
        'GET',
        '/api/v1/metrics?${Uri(queryParameters: query).query}',
      ),
    );
  }

  /// One metric: what it is, and which aggregations it allows.
  Future<MetricDetail> metric(String name) async => MetricDetail.fromJson(
    await _send('GET', '/api/v1/metrics/${Uri.encodeComponent(name)}'),
  );

  /// Its series under [aggregation].
  ///
  /// The aggregation is not this app's choice: which ones are even meaningful
  /// depends on the metric's type, so the detail is read first and its
  /// `default_aggregation` is what gets asked for.
  Future<MetricQueryResponse> metricSeries(
    String name, {
    required MetricAggregation aggregation,
  }) async => MetricQueryResponse.fromJson(
    await _send(
      'POST',
      '/api/v1/metrics/query',
      body: {'metric': name, 'aggregation': aggregation.wire},
    ),
  );

  Future<TracesQueryResponse> traces({
    String q = '',
    bool slowest = false,
    List<Map<String, Object?>> filters = const [],
    int limit = 50,
  }) async {
    final body = <String, Object?>{
      'root_only': true,
      'limit': limit,
      if (slowest) 'sort': 'duration',
    };
    // The search box is still a contains over the service name -- it is
    // what somebody types when looking for a request -- and the builder's
    // conditions are AND-ed with it.
    final all = <Map<String, Object?>>[
      if (q.trim().isNotEmpty)
        {'key': 'service_name', 'op': 'contains', 'value': q.trim()},
      ...filters,
    ];
    if (all.isNotEmpty) body['filters'] = all;
    return TracesQueryResponse.fromJson(
      await _send('POST', '/api/v1/traces/query', body: body),
    );
  }

  /// Recent log records, newest first.
  Future<LogPage> logs({
    String q = '',
    String severityMin = '',
    String service = '',
    String traceId = '',
    String podUid = '',
    String containerId = '',
    String filters = '',
    int limit = 50,
  }) async {
    final query = <String, String>{'limit': '$limit'};
    // The filter builder's conditions, already JSON: the parameter is a
    // JSON array of QueryFilter and the server parses it.
    if (filters.isNotEmpty) query['filters'] = filters;
    if (q.isNotEmpty) query['q'] = q;
    if (severityMin.isNotEmpty) query['severity_min'] = severityMin;
    if (service.isNotEmpty) query['service'] = service;
    // The three that make this screen reachable from somewhere else: a
    // request, a pod or a container has logs, and finding them by typing is
    // the part a phone is worst at.
    if (traceId.isNotEmpty) query['trace_id'] = traceId;
    if (podUid.isNotEmpty) query['k8s_pod_uid'] = podUid;
    if (containerId.isNotEmpty) query['container_id'] = containerId;
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

  /// Parses and plans a query without reading any telemetry.
  ///
  /// A query the server will not run answers 200 here with the reasons in
  /// it: this is how the console can say what is wrong before somebody
  /// spends a minute of ClickHouse on it.
  Future<OqlValidation> validateQuery(String query) async =>
      OqlValidation.fromJson(
        await _send('POST', '/api/v1/query/validate', body: {'query': query}),
      );

  /// The language itself: event types with their attributes, the functions
  /// and the keywords. With [eventType] also that type's frequent map keys
  /// and, for Metric, the metric names.
  Future<OqlSchema> oqlSchema({String eventType = ''}) async =>
      OqlSchema.fromJson(
        await _send(
          'GET',
          _listPath(
            '/api/v1/query/schema',
            '',
            extra: {if (eventType.isNotEmpty) 'event_type': eventType},
          ),
        ),
      );

  // ---- the sections that are a list and nothing more ----
  //
  // Each is one GET with an optional search. They are grouped here rather than
  // spread out because that is all they are; anything that needs more than a
  // page and a query gets its own method above.

  Future<HostPage> hosts({String q = ''}) async =>
      HostPage.fromJson(await _send('GET', _listPath('/api/v1/hosts', q)));

  /// The containers of the window, with the web's filters: one host, one
  /// compose project, one state.
  Future<ContainerPage> containers({
    String q = '',
    String hostId = '',
    String? composeProject,
    String state = '',
  }) async => ContainerPage.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/containers',
        q,
        extra: {
          if (hostId.isNotEmpty) 'host_id': hostId,
          // An empty project is a filter of its own -- the containers that
          // belong to no compose project -- so null means "every one" and
          // "" means "those without".
          'compose_project': ?composeProject,
          if (state.isNotEmpty) 'state': state,
        },
      ),
    ),
  );

  /// The same containers grouped by compose project, which is where the
  /// project filter's list of projects comes from.
  Future<ComposeProjectPage> containerGroups({String hostId = ''}) async =>
      ComposeProjectPage.fromJson(
        await _send(
          'GET',
          _listPath(
            '/api/v1/containers/groups',
            '',
            extra: {if (hostId.isNotEmpty) 'host_id': hostId},
          ),
        ),
      );

  /// The pods of the window, with the web's own filters: a cluster, a
  /// namespace, a phase, one node or one workload's pods.
  Future<PodPage> pods({
    String q = '',
    String clusterUid = '',
    String namespace = '',
    String node = '',
    String phase = '',
    String workloadKind = '',
    String workloadName = '',
  }) async => PodPage.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/kubernetes/pods',
        q,
        extra: {
          if (clusterUid.isNotEmpty) 'cluster_uid': clusterUid,
          if (namespace.isNotEmpty) 'namespace': namespace,
          if (node.isNotEmpty) 'node': node,
          if (phase.isNotEmpty) 'phase': phase,
          if (workloadKind.isNotEmpty) 'workload_kind': workloadKind,
          if (workloadName.isNotEmpty) 'workload_name': workloadName,
        },
      ),
    ),
  );

  /// The organization's cloud connections, each with the last poll per
  /// scope, and whether this server can keep credentials at all.
  Future<CloudConnectionList> cloudConnections() async =>
      CloudConnectionList.fromJson(
        await _send('GET', '/api/v1/cloud/connections'),
      );

  /// One connection's recent polls, newest first: what each one collected,
  /// what it cost in provider requests and what failed.
  Future<CloudRunList> cloudRuns(String id, {int limit = 50}) async =>
      CloudRunList.fromJson(
        await _send(
          'GET',
          '/api/v1/cloud/connections/${Uri.encodeComponent(id)}/runs'
              '?limit=$limit',
        ),
      );

  /// The clusters with data in the window, by name.
  Future<KubernetesClusterPage> k8sClusters() async =>
      KubernetesClusterPage.fromJson(
        await _send('GET', '/api/v1/kubernetes/clusters'),
      );

  /// One cluster: its totals, its workload health by kind and the latest
  /// Warning events.
  Future<KubernetesClusterDetail> k8sCluster(String uid) async =>
      KubernetesClusterDetail.fromJson(
        await _send(
          'GET',
          '/api/v1/kubernetes/clusters/${Uri.encodeComponent(uid)}',
        ),
      );

  Future<KubernetesNodePage> k8sNodes({
    String clusterUid = '',
    String q = '',
  }) async => KubernetesNodePage.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/kubernetes/nodes',
        q,
        extra: {if (clusterUid.isNotEmpty) 'cluster_uid': clusterUid},
      ),
    ),
  );

  Future<KubernetesWorkloadPage> k8sWorkloads({
    String clusterUid = '',
    String namespace = '',
    String kind = '',
    String health = '',
    String q = '',
  }) async => KubernetesWorkloadPage.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/kubernetes/workloads',
        q,
        extra: {
          if (clusterUid.isNotEmpty) 'cluster_uid': clusterUid,
          if (namespace.isNotEmpty) 'namespace': namespace,
          if (kind.isNotEmpty) 'kind': kind,
          if (health.isNotEmpty) 'health': health,
        },
      ),
    ),
  );

  /// The cluster's events, newest first. Warnings are what anybody opens
  /// this for, so the type is a filter rather than a thing to scroll past.
  Future<KubernetesEventList> k8sEvents({
    String clusterUid = '',
    String namespace = '',
    String type = '',
    int limit = 100,
  }) async => KubernetesEventList.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/kubernetes/events',
        '',
        extra: {
          'limit': '$limit',
          if (clusterUid.isNotEmpty) 'cluster_uid': clusterUid,
          if (namespace.isNotEmpty) 'namespace': namespace,
          if (type.isNotEmpty) 'type': type,
        },
      ),
    ),
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

  /// What one instance is busy with: average active sessions per wait type,
  /// the top wait events and the statements the samples were running.
  Future<DbActivity> dbActivity(String instance) async => DbActivity.fromJson(
    await _send(
      'GET',
      _listPath('/api/v1/db/activity', '', extra: {'instance': instance}),
    ),
  );

  /// The statements of an instance, heaviest first by [sort].
  Future<DbQueryPage> dbQueries({
    required String instance,
    String sort = 'time',
    String q = '',
    int limit = 50,
  }) async => DbQueryPage.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/db/queries',
        q,
        extra: {'instance': instance, 'sort': sort, 'limit': '$limit'},
      ),
    ),
  );

  /// One statement: its series, its plans, its waits and the services that
  /// run it.
  Future<DbQueryDetail> dbQuery({
    required String instance,
    required String fingerprint,
  }) async => DbQueryDetail.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/db/queries/${Uri.encodeComponent(fingerprint)}',
        '',
        extra: {'instance': instance},
      ),
    ),
  );

  /// The latest session sample: who is connected, what they are waiting on
  /// and who is blocking whom.
  Future<DbSessionPage> dbSessions(String instance) async =>
      DbSessionPage.fromJson(
        await _send(
          'GET',
          _listPath('/api/v1/db/sessions', '', extra: {'instance': instance}),
        ),
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

  /// The explorer views somebody kept: their own private ones and the
  /// organization's.
  ///
  /// PostgreSQL only; a 404 means this installation cannot keep views,
  /// which is a different thing from having none.
  Future<SavedViewPage> savedViews(String signal) async =>
      SavedViewPage.fromJson(
        await _send(
          'GET',
          _listPath('/api/v1/saved-views', '', extra: {'signal': signal}),
        ),
      );

  /// Keeps one. `state` is the web's own shape, so a view saved here opens
  /// there and the other way round.
  Future<SavedView> createSavedView({
    required String signal,
    required String name,
    required String visibility,
    required Map<String, Object?> state,
    String description = '',
  }) async => SavedView.fromJson(
    await _send(
      'POST',
      '/api/v1/saved-views',
      body: _savedViewBody(
        signal: signal,
        name: name,
        visibility: visibility,
        state: state,
        description: description,
      ),
    ),
  );

  /// Writes over one. The whole view goes back, so the name, the visibility
  /// and the description are sent as they were unless the caller changed
  /// them -- a PUT that left them out would quietly empty them.
  Future<SavedView> updateSavedView(
    String id, {
    required String signal,
    required String name,
    required String visibility,
    required Map<String, Object?> state,
    String description = '',
  }) async => SavedView.fromJson(
    await _send(
      'PUT',
      '/api/v1/saved-views/${Uri.encodeComponent(id)}',
      body: _savedViewBody(
        signal: signal,
        name: name,
        visibility: visibility,
        state: state,
        description: description,
      ),
    ),
  );

  Future<void> deleteSavedView(String id) =>
      _send('DELETE', '/api/v1/saved-views/${Uri.encodeComponent(id)}');

  Map<String, Object?> _savedViewBody({
    required String signal,
    required String name,
    required String visibility,
    required Map<String, Object?> state,
    required String description,
  }) => {
    'signal': signal,
    'name': name,
    'visibility': visibility,
    'state': state,
    if (description.isNotEmpty) 'description': description,
  };

  /// Which series behaved differently in a window than before it.
  ///
  /// The window is required and at most six hours; the baseline defaults to
  /// four window-lengths before it, which is the server's own choice and
  /// not something this app should second-guess.
  Future<MetricCorrelations> correlateMetrics({
    required DateTime from,
    required DateTime to,
    String metric = '',
    String hostId = '',
  }) async => MetricCorrelations.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/metrics/correlate',
        '',
        extra: {
          'from': from.toUtc().toIso8601String(),
          'to': to.toUtc().toIso8601String(),
          if (metric.isNotEmpty) 'metric': metric,
          if (hostId.isNotEmpty) 'host_id': hostId,
        },
      ),
    ),
  );

  /// The traces behind one metric's data points, so a spike can be opened
  /// as a request.
  Future<MetricExemplarsResponse> metricExemplars({
    required String metric,
    List<Map<String, Object?>> filters = const [],
    int limit = 20,
  }) async => MetricExemplarsResponse.fromJson(
    await _send(
      'POST',
      '/api/v1/metrics/exemplars',
      body: {
        'metric': metric,
        if (filters.isNotEmpty) 'filters': filters,
        'limit': limit,
      },
    ),
  );

  /// Spans per bucket for the traces list's conditions, with the p50, p95
  /// and p99 of their duration.
  Future<TracesAggregateResponse> traceVolume({
    String q = '',
    List<Map<String, Object?>> filters = const [],
  }) async => TracesAggregateResponse.fromJson(
    await _send(
      'POST',
      '/api/v1/traces/aggregate',
      body: {
        // The same shape the list is asked with, so the chart is of the
        // rows underneath it rather than of everything.
        'root_only': true,
        if (filters.isNotEmpty || q.isNotEmpty)
          'filters': [
            if (q.trim().isNotEmpty)
              {'key': 'service_name', 'op': 'contains', 'value': q.trim()},
            ...filters,
          ],
      },
    ),
  );

  /// The distinct messages behind the matching logs.
  ///
  /// The processor masks the variable parts of a body into a template, so
  /// this answers "what is being said" rather than "what was said at
  /// 10:04" -- the question a thousand lines a minute makes unanswerable
  /// any other way.
  Future<LogsPatternsResponse> logPatterns({
    String q = '',
    List<Map<String, Object?>> filters = const [],
    int limit = 50,
  }) async => LogsPatternsResponse.fromJson(
    await _send(
      'POST',
      '/api/v1/logs/patterns',
      body: {
        if (q.isNotEmpty) 'q': q,
        if (filters.isNotEmpty) 'filters': filters,
        'limit': limit,
      },
    ),
  );

  /// How many logs arrived when, for the same filters.
  Future<LogsAggregateResponse> logVolume({
    String q = '',
    List<Map<String, Object?>> filters = const [],
    String groupBy = '',
  }) async => LogsAggregateResponse.fromJson(
    await _send(
      'POST',
      '/api/v1/logs/aggregate',
      body: {
        if (q.isNotEmpty) 'q': q,
        if (filters.isNotEmpty) 'filters': filters,
        if (groupBy.isNotEmpty) 'group_by': groupBy,
      },
    ),
  );

  /// The attribute keys one signal has, most frequent first.
  ///
  /// The dictionary behind every filter builder: a key somebody can filter
  /// on is one the data actually has, not one they remembered.
  Future<FieldKeysResponse> fieldKeys({
    required String signal,
    String q = '',
    String metric = '',
  }) async => FieldKeysResponse.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/fields/keys',
        '',
        extra: {
          'signal': signal,
          if (q.isNotEmpty) 'q': q,
          if (metric.isNotEmpty) 'metric': metric,
        },
      ),
    ),
  );

  /// The values of one key, with how often each one occurs.
  Future<FieldValuesResponse> fieldValues({
    required String signal,
    required String key,
    String q = '',
    String metric = '',
  }) async => FieldValuesResponse.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/fields/values',
        '',
        extra: {
          'signal': signal,
          'key': key,
          if (q.isNotEmpty) 'q': q,
          if (metric.isNotEmpty) 'metric': metric,
        },
      ),
    ),
  );

  /// What this account may ask for: an export of its data, a deletion, and
  /// whether either needs a password or a recent SSO sign-in.
  Future<AccountPrivacy> accountPrivacy() async =>
      AccountPrivacy.fromJson(await _send('GET', '/api/v1/account/privacy'));

  /// The exports this account asked for, newest first.
  Future<DataExportList> personalExports() async => DataExportList.fromJson(
    await _send('GET', '/api/v1/account/data-exports'),
  );

  /// Queues one. The file is produced in the background and downloaded
  /// from the web, which is where a file is useful.
  Future<void> requestPersonalExport() =>
      _send('POST', '/api/v1/account/data-exports');

  /// Sends the verification link again, to the address being verified.
  Future<void> resendVerificationEmail() =>
      _send('POST', '/api/v1/auth/verify-email/resend');

  /// Deletes this account. The e-mail is typed again because the server
  /// asks for it, and because this cannot be undone.
  Future<void> deleteAccount({
    required String confirmEmail,
    String password = '',
  }) => _send(
    'POST',
    '/api/v1/account/delete',
    body: {
      'confirm_email': confirmEmail,
      if (password.isNotEmpty) 'password': password,
    },
  );

  /// Schedules the organization's deletion. It runs after a grace period,
  /// and can be cancelled until then.
  Future<void> scheduleOrgDeletion({
    required String confirmName,
    String password = '',
  }) => _send(
    'POST',
    '/api/v1/orgs/current/deletion',
    body: {
      'confirm_name': confirmName,
      if (password.isNotEmpty) 'password': password,
    },
  );

  Future<void> cancelOrgDeletion(String id) =>
      _send('POST', '/api/v1/org-deletions/${Uri.encodeComponent(id)}/cancel');

  /// Every single sign-on connection, and what the identity provider has
  /// to be told about this installation.
  Future<SSOState> ssoConnections() async =>
      SSOState.fromJson(await _send('GET', '/api/v1/sso/connections'));

  /// Turns one on or off. The whole connection is not sent back: this
  /// endpoint takes the one field.
  Future<void> setSsoConnectionEnabled(String id, {required bool enabled}) =>
      _send(
        'PATCH',
        '/api/v1/sso/connections/${Uri.encodeComponent(id)}',
        body: {'enabled': enabled},
      );

  /// What the server can check about a connection without anybody signing
  /// in: the discovery document, the certificates, the clock.
  Future<SSOTestResult> testSsoConnection(String id) async =>
      SSOTestResult.fromJson(
        await _send(
          'POST',
          '/api/v1/sso/connections/${Uri.encodeComponent(id)}/test',
        ),
      );

  /// Which e-mail domains sign in through SSO.
  Future<SSODomainPage> ssoDomains() async =>
      SSODomainPage.fromJson(await _send('GET', '/api/v1/sso/domains'));

  Future<void> addSsoDomain(String domain) =>
      _send('POST', '/api/v1/sso/domains', body: {'domain': domain});

  /// Asks the server to check a domain, by DNS or by e-mail.
  Future<SSODomain> verifySsoDomain(
    String id, {
    required String method,
    String emailLocalPart = '',
  }) async => SSODomain.fromJson(
    await _send(
      'POST',
      '/api/v1/sso/domains/${Uri.encodeComponent(id)}/verify',
      body: {
        'method': method,
        if (emailLocalPart.isNotEmpty) 'email_local_part': emailLocalPart,
      },
    ),
  );

  Future<void> deleteSsoDomain(String id) =>
      _send('DELETE', '/api/v1/sso/domains/${Uri.encodeComponent(id)}');

  /// Whether everyone has to sign in through SSO, and who may not.
  Future<SSOState> setSsoEnforcement({
    required bool enforce,
    required List<String> breakGlassUserIds,
  }) async => SSOState.fromJson(
    await _send(
      'PUT',
      '/api/v1/sso/enforcement',
      body: {
        'enforce': enforce,
        // Sent whole, because the endpoint replaces the list: leaving it
        // out would empty it and lock everybody into the IdP.
        'break_glass_user_ids': breakGlassUserIds,
      },
    ),
  );

  /// Which IdP group becomes which role.
  Future<SSORoleMappingPage> ssoRoleMappings() async =>
      SSORoleMappingPage.fromJson(
        await _send('GET', '/api/v1/sso/role-mappings'),
      );

  /// Replaces the whole set, as the endpoint does.
  Future<void> putSsoRoleMappings(List<SSORoleMapping> mappings) => _send(
    'PUT',
    '/api/v1/sso/role-mappings',
    body: {
      'mappings': [for (final m in mappings) m.toJson()],
    },
  );

  /// The tokens an identity provider provisions users with.
  Future<ScimTokenPage> scimTokens() async =>
      ScimTokenPage.fromJson(await _send('GET', '/api/v1/scim/tokens'));

  Future<void> revokeScimToken(String id) =>
      _send('DELETE', '/api/v1/scim/tokens/${Uri.encodeComponent(id)}');

  /// How full the ClickHouse disks are, and the levels they are reported
  /// at. Admin and above; the disks belong to the operator.
  Future<DiskSpace> diskSpace() async =>
      DiskSpace.fromJson(await _send('GET', '/api/v1/storage/disk'));

  /// What the organization used this billing period, against its plan.
  ///
  /// [period] is `current`, `previous` or a `YYYY-MM` month, as the server
  /// names them.
  Future<UsageOverview> usage({String period = ''}) async =>
      UsageOverview.fromJson(
        await _send(
          'GET',
          _listPath(
            '/api/v1/usage',
            '',
            extra: {if (period.isNotEmpty) 'period': period},
          ),
        ),
      );

  /// The organization itself: its name, its ids and the language the
  /// server writes in for everyone in it.
  Future<Organization> currentOrg() async =>
      Organization.fromJson(await _send('GET', '/api/v1/orgs/current'));

  /// Renames it, or changes that language. Both go in one PATCH because
  /// the endpoint takes both; sending only what changed keeps the other
  /// one as it is.
  Future<Organization> updateCurrentOrg({
    String? name,
    String? language,
  }) async => Organization.fromJson(
    await _send(
      'PATCH',
      '/api/v1/orgs/current',
      body: {'name': ?name, 'language': ?language},
    ),
  );

  /// The stored source maps, newest first. The documents themselves are
  /// never served back -- they are read only to un-minify a stack.
  Future<SourceMapPage> sourceMaps() async =>
      SourceMapPage.fromJson(await _send('GET', '/api/v1/source-maps'));

  Future<void> deleteSourceMap(String id) =>
      _send('DELETE', '/api/v1/source-maps/${Uri.encodeComponent(id)}');

  /// What changed in the organization, newest first.
  ///
  /// [cursor] continues the previous page and must be used with the same
  /// filters: the server builds it from them.
  Future<AuditLogPage> auditLog({
    String actor = '',
    String action = '',
    String cursor = '',
    int limit = 50,
  }) async => AuditLogPage.fromJson(
    await _send(
      'GET',
      _listPath(
        '/api/v1/audit-log',
        '',
        extra: {
          if (actor.isNotEmpty) 'actor': actor,
          if (action.isNotEmpty) 'action': action,
          if (cursor.isNotEmpty) 'cursor': cursor,
          'limit': '$limit',
        },
      ),
    ),
  );

  /// The ingest license keys, including revoked ones.
  Future<LicenseKeyPage> licenseKeys() async =>
      LicenseKeyPage.fromJson(await _send('GET', '/api/v1/license-keys'));

  /// Makes one. The generated value comes back once and never again.
  Future<LicenseKeyCreated> createLicenseKey(String name) async =>
      LicenseKeyCreated.fromJson(
        await _send('POST', '/api/v1/license-keys', body: {'name': name}),
      );

  /// Revokes one. Ingest keeps accepting it for up to the auth cache TTL,
  /// which is the server's own behaviour and worth saying on screen.
  Future<void> revokeLicenseKey(String id) =>
      _send('DELETE', '/api/v1/license-keys/${Uri.encodeComponent(id)}');

  Future<ApiKeyPage> apiKeys() async =>
      ApiKeyPage.fromJson(await _send('GET', '/api/v1/api-keys'));

  Future<ApiKeyCreated> createApiKey({
    required String name,
    required String role,
  }) async => ApiKeyCreated.fromJson(
    await _send('POST', '/api/v1/api-keys', body: {'name': name, 'role': role}),
  );

  /// Revokes one, effective immediately.
  Future<void> revokeApiKey(String id) =>
      _send('DELETE', '/api/v1/api-keys/${Uri.encodeComponent(id)}');

  Future<BrowserKeyPage> browserKeys() async =>
      BrowserKeyPage.fromJson(await _send('GET', '/api/v1/browser-keys'));

  /// Makes one.
  ///
  /// Exactly one allowlist goes with the kind -- `origins` for a browser
  /// key, `app_ids` for a mobile one -- and sending the other, or neither,
  /// is a 400. The screen asks for the one that belongs to the choice.
  Future<BrowserKeyCreated> createBrowserKey({
    required String name,
    required String serviceName,
    required String kind,
    required List<String> allowlist,
    String environment = '',
  }) async => BrowserKeyCreated.fromJson(
    await _send(
      'POST',
      '/api/v1/browser-keys',
      body: {
        'name': name,
        'service_name': serviceName,
        if (environment.isNotEmpty) 'environment': environment,
        'kind': kind,
        if (kind == 'mobile') 'app_ids': allowlist else 'origins': allowlist,
      },
    ),
  );

  Future<void> revokeBrowserKey(String id) =>
      _send('DELETE', '/api/v1/browser-keys/${Uri.encodeComponent(id)}');

  /// Who is in the organization.
  Future<MemberPage> members() async =>
      MemberPage.fromJson(await _send('GET', '/api/v1/members'));

  /// Changes somebody's role. The server refuses to leave an organization
  /// without an owner, which is the 409 this can answer with.
  Future<void> setMemberRole(String userId, String role) => _send(
    'PATCH',
    '/api/v1/members/${Uri.encodeComponent(userId)}',
    body: {'role': role},
  );

  /// Removes a member. Any member may remove themselves, which is leaving.
  Future<void> removeMember(String userId) =>
      _send('DELETE', '/api/v1/members/${Uri.encodeComponent(userId)}');

  /// The invitations nobody has accepted yet.
  Future<InvitationPage> invitations({bool includeExpired = false}) async =>
      InvitationPage.fromJson(
        await _send(
          'GET',
          _listPath(
            '/api/v1/invitations',
            '',
            extra: {if (includeExpired) 'include_expired': 'true'},
          ),
        ),
      );

  /// Invites somebody. The token comes back once and never again, so what
  /// the screen does with it is the only chance to pass it on.
  Future<InvitationCreated> createInvitation({
    required String email,
    required String role,
  }) async => InvitationCreated.fromJson(
    await _send(
      'POST',
      '/api/v1/invitations',
      body: {'email': email, 'role': role},
    ),
  );

  /// A new token and a new expiry; the previous link stops working.
  Future<InvitationCreated> resendInvitation(String id) async =>
      InvitationCreated.fromJson(
        await _send(
          'POST',
          '/api/v1/invitations/${Uri.encodeComponent(id)}/resend',
        ),
      );

  Future<void> revokeInvitation(String id) =>
      _send('DELETE', '/api/v1/invitations/${Uri.encodeComponent(id)}');

  /// Changes the password. The server revokes the person's other sessions,
  /// which is why this screen says so before asking.
  Future<void> changePassword({
    required String currentPassword,
    required String newPassword,
  }) => _send(
    'POST',
    '/api/v1/auth/password',
    body: {'current_password': currentPassword, 'new_password': newPassword},
  );

  /// Sets the language the server writes in: alert e-mails, generated rule
  /// names. Not the app's own language, which follows the phone.
  Future<Me> setMyLanguage(String language) async => Me.fromJson(
    await _send('PATCH', '/api/v1/auth/me', body: {'language': language}),
  );

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
