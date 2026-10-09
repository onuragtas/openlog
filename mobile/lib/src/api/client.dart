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

  /// How far behind the agents are, fleet-wide.
  Future<FleetSummary> fleetSummary() async =>
      FleetSummary.fromJson(await _send('GET', '/api/v1/fleet/summary'));

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
    String service = '',
    String traceId = '',
    String podUid = '',
    String containerId = '',
    int limit = 50,
  }) async {
    final query = <String, String>{'limit': '$limit'};
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
