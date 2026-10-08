// The screens, against a controller that only records what it was asked to do.
// The controller's own behaviour is tested in session_test.dart against a real
// server; what these check is the wiring in between -- that the button calls
// the right thing with what was typed, and that a screen only offers what the
// server allows.
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:openlog_mobile/main.dart';
import 'package:openlog_mobile/src/api/client.dart';
import 'package:openlog_mobile/src/api/schema.g.dart';
import 'package:openlog_mobile/src/alerts.dart';
import 'package:openlog_mobile/src/dashboards.dart';
import 'package:openlog_mobile/src/detail.dart';
import 'package:openlog_mobile/src/sections.dart';
import 'package:openlog_mobile/src/logs.dart';
import 'package:openlog_mobile/src/query.dart';
import 'package:openlog_mobile/src/services.dart';
import 'package:openlog_mobile/src/session.dart';
import 'package:openlog_mobile/src/storage/token_store.dart';

AuthConfig config({
  bool signup = true,
  AuthConfigCaptcha? captcha,
  AuthConfigMode mode = AuthConfigMode.postgres,
}) => AuthConfig(
  mode: mode,
  signupEnabled: signup,
  passwordMinLength: 12,
  emailEnabled: true,
  emailVerificationRequired: false,
  captcha: captcha,
);

OrgRef org(String id, String name) =>
    OrgRef(id: id, tenantId: 't$id', name: name, role: Role.owner);

Me me({List<OrgRef> orgs = const []}) => Me(
  auth: MeAuth.device,
  user: const User(
    id: 'u1',
    email: 'owner@example.com',
    name: 'Onur',
    emailVerified: true,
    language: UserLanguage.tr,
  ),
  organization: orgs.isEmpty ? org('o1', 'Org A') : orgs.first,
  role: Role.owner,
  organizations: orgs.isEmpty ? [org('o1', 'Org A')] : orgs,
);

/// An alerts controller that makes no requests. The controller's own behaviour
/// is covered in alerts_test.dart against a real server; here it only has to
/// hold a state for the screen to draw.
class ScriptedAlerts extends AlertsController {
  ScriptedAlerts({
    List<AlertIncident> incidents = const [],
    IncidentPageCounts? counts,
  }) : super(OpenlogClient(baseUrl: 'http://127.0.0.1:1')) {
    items = incidents;
    this.counts =
        counts ??
        const IncidentPageCounts(open: 0, acknowledged: 0, resolved: 0);
    loaded = true;
  }

  final calls = <String>[];

  @override
  Future<void> refresh() async => calls.add('refresh');

  @override
  Future<void> acknowledge(String id) async => calls.add('acknowledge:$id');
}

AlertIncident firing(
  String id, {
  String state = 'open',
  AlertSeverity severity = AlertSeverity.critical,
  String? ackBy,
}) => AlertIncident(
  id: id,
  ruleName: 'API error rate',
  ruleType: AlertRuleType.unknown,
  severity: severity,
  state: state == 'open'
      ? AlertIncidentState.open
      : AlertIncidentState.acknowledged,
  seriesKey: 'service.name=checkout',
  labels: const {'service.name': 'checkout'},
  summary: 'error rate 12% over 5m',
  flapping: false,
  muted: false,
  openedAt: DateTime.now().toUtc().subtract(const Duration(minutes: 7)),
  acknowledgedByEmail: ackBy,
  channelIds: const [],
);

/// Services and logs that make no requests, for the same reason as above.
class ScriptedServices extends ServicesController {
  ScriptedServices({List<ApmService> services = const []})
    : super(OpenlogClient(baseUrl: 'http://127.0.0.1:1')) {
    items = services;
    loaded = true;
  }

  final calls = <String>[];

  @override
  Future<void> refresh() async => calls.add('refresh:$query');
}

class ScriptedLogs extends LogsController {
  ScriptedLogs({List<LogRecord> logs = const []})
    : super(OpenlogClient(baseUrl: 'http://127.0.0.1:1')) {
    items = logs;
    loaded = true;
  }

  final calls = <String>[];

  @override
  Future<void> refresh() async => calls.add('refresh:$query:$severityMin');
}

ApmService service(
  String name, {
  double errorRate = 0,
  double throughput = 120,
  double? p95 = 180,
}) => ApmService(
  requests: 1200,
  throughput: throughput,
  errors: 3,
  errorRate: errorRate,
  p95Ms: p95,
  apdex: 0.97,
  serviceName: name,
  serviceNamespace: '',
  environment: 'prod',
  language: 'go',
  version: '1.2.3',
  lastSeen: DateTime.now().toUtc(),
  apdexTMs: 500,
  sparkline: const [],
);

LogRecord logLine(
  String body, {
  int severity = 17,
  String service = 'checkout',
}) => LogRecord(
  timestamp: DateTime.now().toUtc().subtract(const Duration(minutes: 2)),
  severityText: severity >= 17 ? 'ERROR' : 'WARN',
  severityNumber: severity,
  body: body,
  hostId: 'h1',
  serviceName: service,
  traceId: '',
  spanId: '',
  attributes: const {},
  resourceAttributes: const {},
);

class ScriptedDashboards extends DashboardsController {
  ScriptedDashboards({List<DashboardSummary> dashboards = const []})
    : super(OpenlogClient(baseUrl: 'http://127.0.0.1:1')) {
    items = dashboards;
    loaded = true;
  }

  final calls = <String>[];

  @override
  Future<void> refresh() async => calls.add('refresh:$query');
}

DashboardSummary board(
  String id,
  String name, {
  int widgets = 4,
  int pages = 1,
}) => DashboardSummary(
  id: id,
  name: name,
  description: '',
  visibility: DashboardVisibility.unknown,
  pageCount: pages,
  widgetCount: widgets,
  createdByEmail: 'ada@example.com',
  updatedAt: DateTime.now().toUtc(),
  canEdit: false,
);

class ScriptedHosts extends HostsController {
  ScriptedHosts() : super(OpenlogClient(baseUrl: 'http://127.0.0.1:1'));

  final calls = <String>[];

  @override
  Future<void> refresh() async {
    calls.add('refresh');
    loaded = true;
  }
}

/// The signed-in app with every section scripted, which is what the shell
/// needs: it builds all of them at once so moving between them keeps state.
Widget signedInApp(
  SessionController session, {
  AlertsController? alerts,
  ServicesController? services,
  LogsController? logs,
  DashboardsController? dashboards,
  HostsController? hosts,
  QueryController? query,
  TracesController? traces,
  IncidentController Function(String id)? incident,
  ServiceOverviewController Function(String name)? serviceOverview,
  ServiceErrorsController Function(String name)? serviceErrors,
  TraceController Function(String traceId)? trace,
}) => OpenlogApp(
  store: MemoryTokenStore(),
  session: session,
  sections: Sections(
    // Never reached: every controller below is either scripted or one of the
    // plain ones, which this test never refreshes.
    client: OpenlogClient(baseUrl: 'http://127.0.0.1:1'),
    alerts: alerts ?? ScriptedAlerts(),
    services: services ?? ScriptedServices(),
    logs: logs ?? ScriptedLogs(),
    dashboards: dashboards ?? ScriptedDashboards(),
    query: query,
    traces: traces,
    hosts: hosts,
    incident: incident,
    serviceOverview: serviceOverview,
    serviceErrors: serviceErrors,
    trace: trace,
  ),
);

/// Unlike the other scripted controllers this one starts empty and fills on
/// refresh, because what the errors tab is tested for is *when* it asks: a
/// fake that pretends to be loaded already would make the question unaskable.
class ScriptedTraces extends TracesController {
  ScriptedTraces(List<SpanQueryRow> rows)
    : super(OpenlogClient(baseUrl: 'http://127.0.0.1:1')) {
    items = rows;
    loaded = true;
  }

  final calls = <String>[];

  @override
  Future<void> refresh() async => calls.add('refresh:$query:$slowest');
}

SpanQueryRow spanRow(String traceId, {bool error = false}) => SpanQueryRow(
  id: traceId,
  timestamp: DateTime.now().toUtc().subtract(const Duration(minutes: 1)),
  traceId: traceId,
  spanId: 's1',
  parentSpanId: '',
  name: 'POST /checkout',
  kind: SpanKind.server,
  statusCode: error ? SpanStatusCode.error : SpanStatusCode.ok,
  statusMessage: '',
  serviceName: 'checkout',
  hostId: 'h1',
  durationNs: 42000000,
  durationMs: 42,
  isEntry: true,
  isError: error,
  httpStatusCode: error ? 500 : 200,
  transactionName: 'POST /checkout',
  fields: const {},
);

class ScriptedQuery extends QueryController {
  ScriptedQuery({this.answer, this.rejectWith})
    : super(OpenlogClient(baseUrl: 'http://127.0.0.1:1'));

  final OqlResult? answer;
  final String? rejectWith;
  final calls = <String>[];

  @override
  Future<void> run(String query) async {
    final q = query.trim();
    if (q.isEmpty) return;
    calls.add(q);
    if (rejectWith != null) {
      failure = SessionFailure('queryRejected', rejectWith!);
      result = null;
    } else {
      result = answer;
      ran = q;
      history
        ..remove(q)
        ..insert(0, q);
    }
    notifyListeners();
  }
}

OqlResult singleAnswer(double v) => OqlResult(
  kind: OqlResultKind.single,
  eventType: 'logs',
  columns: const [
    OqlColumn(name: 'count', function: 'count', type: OqlColumnType.number),
  ],
  facets: const [],
  rows: [
    OqlRow(facets: const [], values: [v]),
  ],
  series: const [],
  buckets: const [],
  metadata: OqlMetadata(
    from: DateTime.utc(2026, 10, 7, 19),
    to: DateTime.utc(2026, 10, 7, 20),
    rollup: false,
    table: 'logs',
    rowsRead: 1200,
    bytesRead: 99000,
    elapsedMs: 34,
    queries: 1,
    facetLimit: 20,
    truncated: false,
    warnings: const [],
  ),
);

class ScriptedErrors extends ServiceErrorsController {
  ScriptedErrors(String name, this._inbox)
    : super(OpenlogClient(baseUrl: 'http://127.0.0.1:1'), name);

  final ApmErrorInbox? _inbox;
  final calls = <String>[];

  @override
  Future<void> refresh() async {
    calls.add('refresh');
    value = _inbox;
    loaded = true;
    notifyListeners();
  }
}

class ScriptedTrace extends TraceController {
  ScriptedTrace(String id, Trace? t)
    : super(OpenlogClient(baseUrl: 'http://127.0.0.1:1'), id) {
    value = t;
    loaded = true;
  }

  @override
  Future<void> refresh() async {}
}

ApmErrorGroup errorGroup(
  String id, {
  String traceId = 'abcdef',
  ApmErrorStatus status = ApmErrorStatus.unresolved,
}) => ApmErrorGroup(
  groupId: id,
  serviceName: 'checkout',
  serviceNamespace: '',
  environment: 'production',
  errorType: 'TimeoutError',
  message: 'upstream timed out',
  count: 12,
  totalCount: 40,
  lastSeen: DateTime.now().toUtc().subtract(const Duration(minutes: 2)),
  lastTraceId: traceId,
  lastSpanName: 'POST /checkout',
  sparkline: const [],
  status: status,
  resolvedInVersion: '',
  resolvedByEmail: '',
  regressionCount: 0,
  commentCount: 0,
  updatedByEmail: '',
);

ApmErrorInbox inboxOf(List<ApmErrorGroup> groups) => ApmErrorInbox(
  step: '1m',
  groups: groups,
  counts: const ApmErrorInboxCounts(unresolved: 1, resolved: 0, ignored: 0),
  truncated: false,
  workflow: true,
);

Trace traceOf() => Trace(
  traceId: 'abcdef',
  spans: [
    Span(
      spanId: 'root',
      parentSpanId: '',
      name: 'POST /checkout',
      kind: SpanKind.server,
      serviceName: 'checkout',
      start: DateTime.utc(2026, 10, 7, 20),
      durationNs: 100000000,
      statusCode: SpanStatusCode.error,
      statusMessage: 'upstream timed out',
      attributes: const {},
      resourceAttributes: const {},
      events: const [],
    ),
  ],
);

/// An incident detail that answers from memory, so a test can check the screen
/// without a server and still see which actions the screen asked for.
class ScriptedIncident extends IncidentController {
  ScriptedIncident(this.detail, {this.failOnResolve = false})
    : super(OpenlogClient(baseUrl: 'http://127.0.0.1:1'), detail.id) {
    value = detail;
    loaded = true;
  }

  final AlertIncidentDetail detail;
  final bool failOnResolve;
  final calls = <String>[];

  @override
  Future<void> refresh() async => calls.add('refresh');

  @override
  Future<void> acknowledge() async => calls.add('acknowledge');

  @override
  Future<void> resolve({String note = ''}) async {
    calls.add('resolve:$note');
    if (failOnResolve) {
      failure = const SessionFailure('alreadyResolved', '');
      notifyListeners();
    }
  }

  @override
  Future<void> addNote(String text) async => calls.add('note:$text');
}

class ScriptedOverview extends ServiceOverviewController {
  ScriptedOverview(String name, ApmOverview? overview)
    : super(OpenlogClient(baseUrl: 'http://127.0.0.1:1'), name) {
    value = overview;
    loaded = true;
  }

  final calls = <String>[];

  @override
  Future<void> refresh() async => calls.add('refresh');
}

/// The incident the list row points at, with a timeline and one failed
/// notification -- the two things the detail screen exists to show.
AlertIncidentDetail detailOf(
  String id, {
  AlertIncidentState state = AlertIncidentState.open,
  Map<String, String> labels = const {
    'service.name': 'checkout',
    'alert.rule': 'r1',
  },
}) => AlertIncidentDetail(
  id: id,
  ruleName: 'API error rate',
  ruleType: AlertRuleType.unknown,
  severity: AlertSeverity.critical,
  state: state,
  seriesKey: 'service.name=checkout',
  labels: labels,
  summary: 'error rate 12% over 5m',
  value: 0.12,
  lastValue: 0.14,
  threshold: 0.05,
  flapping: false,
  muted: false,
  openedAt: DateTime.now().toUtc().subtract(const Duration(minutes: 7)),
  channelIds: const [],
  events: [
    AlertIncidentEvent(
      id: 1,
      at: DateTime.now().toUtc().subtract(const Duration(minutes: 7)),
      kind: AlertIncidentEventKind.opened,
      message: 'error rate 12% over 5m',
      details: const {},
    ),
  ],
  deliveries: [
    AlertDelivery(
      id: 'd1',
      ruleName: 'API error rate',
      channelName: 'oncall-email',
      channelType: AlertChannelType.email,
      kind: AlertNotificationKind.unknown,
      status: AlertNotificationStatus.failed,
      attempts: 3,
      idempotencyKey: 'k1',
      createdAt: DateTime.now().toUtc(),
      lastError: 'smtp: connection refused',
      attemptLog: const [],
    ),
  ],
);

ApmOverview overviewOf({double requests = 1200, double errorRate = 0.12}) =>
    ApmOverview(
      step: '1m',
      apdexTMs: 500,
      totals: ApmRed(
        requests: requests,
        throughput: requests / 60,
        errors: requests * errorRate,
        errorRate: errorRate,
        p50Ms: 42,
        p95Ms: 310,
        p99Ms: 980,
        apdex: 0.91,
      ),
      series: [
        for (var i = 0; i < 4; i++)
          ApmPoint(
            requests: requests / 4,
            throughput: requests / 240,
            errors: 1,
            errorRate: errorRate,
            t: i,
          ),
      ],
    );

/// Opens the navigation drawer, which is how the web moves between sections
/// below `lg` and now how this app does too.
Future<void> openDrawer(WidgetTester tester) async {
  tester.state<ScaffoldState>(find.byType(Scaffold).last).openDrawer();
  await tester.pumpAndSettle();
}

/// Goes to a section by its drawer entry, which is keyed by its label.
///
/// Scrolls first when it has to: the drawer holds seventeen entries and the
/// default test surface is 600 points tall, so the last few are not built
/// until the list is scrolled -- which looked exactly like "Settings is
/// missing from the drawer".
Future<void> goTo(WidgetTester tester, String label) async {
  await openDrawer(tester);
  final item = find.byKey(Key('nav-$label'));
  if (item.evaluate().isEmpty) {
    await tester.scrollUntilVisible(
      item,
      120,
      scrollable: find
          .descendant(
            of: find.byType(Drawer),
            matching: find.byType(Scrollable),
          )
          .first,
    );
  }
  await tester.tap(item);
  await tester.pumpAndSettle();
}

/// Records calls instead of making them.
class ScriptedSession extends SessionController {
  ScriptedSession({
    required SessionStage stage,
    this.address = 'https://openlog.example.com',
  }) : super(store: MemoryTokenStore()) {
    this.stage = stage;
  }

  final String address;
  final calls = <String>[];

  @override
  String? get baseUrl => stage == SessionStage.needsServer ? null : address;

  @override
  Future<void> restore() async {}

  @override
  Future<void> useServer(String input) async => calls.add('useServer:$input');

  @override
  Future<void> signIn({
    required String email,
    required String password,
    required String deviceName,
  }) async => calls.add('signIn:$email:$password:$deviceName');

  @override
  Future<void> signUp({
    required String email,
    required String password,
    required String name,
    required String organizationName,
    required String deviceName,
  }) async => calls.add('signUp:$email:$organizationName:$name');

  @override
  Future<void> signOut() async => calls.add('signOut');

  @override
  Future<void> switchOrganization(String orgId) async =>
      calls.add('switchOrganization:$orgId');

  @override
  Future<void> forgetServer() async => calls.add('forgetServer');
}

Future<void> pumpApp(
  WidgetTester tester,
  ScriptedSession session, {
  Locale? locale,
}) async {
  await tester.pumpWidget(
    Localizations.override(
      context: tester.binding.rootElement!,
      locale: locale,
      child: OpenlogApp(store: MemoryTokenStore(), session: session),
    ),
  );
  await tester.pump();
}

void main() {
  // A phone, not the 800x600 desktop window flutter_test defaults to. The
  // screens are built for this width, and a layout that only fits on a desktop
  // surface is a layout that overflows on the device it ships to.
  setUp(() {
    final view = TestWidgetsFlutterBinding.ensureInitialized()
        .platformDispatcher
        .views
        .first;
    view.physicalSize = const Size(390 * 3, 844 * 3);
    view.devicePixelRatio = 3;
  });

  tearDown(() {
    final view = TestWidgetsFlutterBinding.ensureInitialized()
        .platformDispatcher
        .views
        .first;
    view.resetPhysicalSize();
    view.resetDevicePixelRatio();
  });

  /// Taps something that may be below the fold on a phone.
  Future<void> tapScrolled(WidgetTester tester, Key key) async {
    await tester.ensureVisible(find.byKey(key));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(key));
    await tester.pump();
  }

  testWidgets(
    'while the stored session is being checked, nothing is decided yet',
    (tester) async {
      final s = ScriptedSession(stage: SessionStage.restoring);
      await tester.pumpWidget(
        OpenlogApp(store: MemoryTokenStore(), session: s),
      );
      await tester.pump();

      expect(find.byType(CircularProgressIndicator), findsOneWidget);
      // No sign-in form flashes before the check comes back.
      expect(find.byKey(const Key('email')), findsNothing);
    },
  );

  testWidgets(
    'the first screen offers the hosted address and sends what was typed',
    (tester) async {
      final s = ScriptedSession(stage: SessionStage.needsServer);
      await tester.pumpWidget(
        OpenlogApp(store: MemoryTokenStore(), session: s),
      );
      await tester.pump();

      final field = find.byKey(const Key('server-address'));
      expect(tester.widget<TextField>(field).controller!.text, defaultBaseUrl);

      await tester.enterText(field, 'openlog.lan:8080');
      await tester.tap(find.byKey(const Key('server-continue')));
      await tester.pump();

      expect(s.calls, ['useServer:openlog.lan:8080']);
    },
  );

  testWidgets('sign-in sends the e-mail, password and device name', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.needsSignIn)
      ..authConfig = config();
    await tester.pumpWidget(OpenlogApp(store: MemoryTokenStore(), session: s));
    await tester.pump();

    await tester.enterText(find.byKey(const Key('email')), 'owner@example.com');
    await tester.enterText(find.byKey(const Key('password')), 'a password');
    await tester.enterText(
      find.byKey(const Key('device-name')),
      "Onur's iPhone",
    );
    await tapScrolled(tester, const Key('submit'));

    expect(s.calls, ["signIn:owner@example.com:a password:Onur's iPhone"]);
  });

  testWidgets('an empty form is answered on the spot, not by a round trip', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.needsSignIn)
      ..authConfig = config();
    await tester.pumpWidget(OpenlogApp(store: MemoryTokenStore(), session: s));
    await tester.pump();

    await tapScrolled(tester, const Key('submit'));

    expect(s.calls, isEmpty);
    expect(
      find.text('E-mail address and password are required.'),
      findsOneWidget,
    );
  });

  // Two tests rather than two pumpWidget calls in one: OpenlogApp takes its
  // controller in initState, so pumping it again with another one keeps the
  // first -- which is right for the app and wrong for a test that wants two.
  testWidgets('sign-up is not offered on a server that has closed it', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.needsSignIn)
      ..authConfig = config(signup: false);
    await tester.pumpWidget(OpenlogApp(store: MemoryTokenStore(), session: s));
    await tester.pump();

    // Showing the link on a server with sign-up closed turns its own rule into
    // an error the person only finds after filling the form in.
    expect(find.byKey(const Key('toggle-signup')), findsNothing);
  });

  testWidgets('sign-up is offered on a server that allows it', (tester) async {
    final s = ScriptedSession(stage: SessionStage.needsSignIn)
      ..authConfig = config();
    await tester.pumpWidget(OpenlogApp(store: MemoryTokenStore(), session: s));
    await tester.pump();

    expect(find.byKey(const Key('toggle-signup')), findsOneWidget);
  });

  testWidgets('creating an account asks for the organization it will own', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.needsSignIn)
      ..authConfig = config();
    await tester.pumpWidget(OpenlogApp(store: MemoryTokenStore(), session: s));
    await tester.pump();

    await tapScrolled(tester, const Key('toggle-signup'));

    // The contract requires organization_name, so this button creates an
    // installation owner and the screen has to say so.
    expect(find.byKey(const Key('signup-org')), findsOneWidget);
    expect(find.byKey(const Key('signup-name')), findsOneWidget);

    await tester.enterText(find.byKey(const Key('email')), 'new@example.com');
    await tester.enterText(
      find.byKey(const Key('password')),
      'a long password',
    );
    await tester.enterText(find.byKey(const Key('signup-org')), 'Acme');
    await tester.enterText(find.byKey(const Key('signup-name')), 'Onur');
    await tapScrolled(tester, const Key('submit'));

    expect(s.calls, ['signUp:new@example.com:Acme:Onur']);
  });

  testWidgets(
    'a server with no user accounts says so instead of offering a form',
    (tester) async {
      final s = ScriptedSession(stage: SessionStage.needsSignIn)
        ..authConfig = config(mode: AuthConfigMode.static);
      await tester.pumpWidget(
        OpenlogApp(store: MemoryTokenStore(), session: s),
      );
      await tester.pump();

      expect(find.byKey(const Key('email')), findsNothing);
      expect(find.textContaining('OPENLOG_AUTH_MODE=static'), findsOneWidget);
      // Changing server is still possible: it is the way out of this screen.
      expect(find.byKey(const Key('change-server')), findsOneWidget);
    },
  );

  testWidgets(
    'signed in, the screen names the person, the organization and the role',
    (tester) async {
      final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
      await tester.pumpWidget(signedInApp(s));
      await tester.pump();
      await goTo(tester, 'Settings');

      expect(find.text('Signed in as owner@example.com'), findsOneWidget);
      expect(find.text('Org A'), findsOneWidget);
      expect(find.text('Owner'), findsOneWidget);
      // One organization is not a choice, so no picker.
      expect(find.byKey(const Key('org-picker')), findsNothing);
    },
  );

  testWidgets('a person in several organizations gets a picker that switches', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)
      ..me = me(orgs: [org('o1', 'Org A'), org('o2', 'Org B')]);
    await tester.pumpWidget(signedInApp(s));
    await tester.pump();
    await goTo(tester, 'Settings');

    await tester.tap(find.byKey(const Key('org-picker')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Org B').last);
    await tester.pumpAndSettle();

    expect(s.calls, ['switchOrganization:o2']);
  });

  testWidgets('signing out is one tap from the signed-in screen', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    await tester.pumpWidget(signedInApp(s));
    await tester.pump();
    await openDrawer(tester);

    await tester.tap(find.byKey(const Key('sign-out')));
    await tester.pump();
    expect(s.calls, ['signOut']);
  });

  testWidgets('a failure is shown in words, not as a silent no-op', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.needsSignIn)
      ..authConfig = config()
      ..failure = const SessionFailure('badCredentials', '');
    await tester.pumpWidget(OpenlogApp(store: MemoryTokenStore(), session: s));
    await tester.pump();

    expect(find.text('Wrong e-mail address or password.'), findsOneWidget);
  });

  // The app builds its own MaterialApp, so an outer one's `locale` is ignored:
  // the locale has to come from the platform, the way it does on a phone. An
  // earlier version of this test wrapped the app instead and passed without
  // ever rendering a Turkish word.
  testWidgets('Turkish comes from the device locale, not from a wrapper', (
    tester,
  ) async {
    tester.platformDispatcher.localesTestValue = const [Locale('tr')];
    addTearDown(tester.platformDispatcher.clearLocalesTestValue);

    final s = ScriptedSession(stage: SessionStage.needsServer);
    await tester.pumpWidget(OpenlogApp(store: MemoryTokenStore(), session: s));
    await tester.pump();

    expect(find.text("openlog'a bağlan"), findsOneWidget);
    expect(find.text('Sunucu adresi'), findsOneWidget);
    expect(find.text('Devam et'), findsOneWidget);
    expect(find.text('Connect to openlog'), findsNothing);
  });

  testWidgets('English is what a device in any other language gets', (
    tester,
  ) async {
    tester.platformDispatcher.localesTestValue = const [Locale('de')];
    addTearDown(tester.platformDispatcher.clearLocalesTestValue);

    final s = ScriptedSession(stage: SessionStage.needsServer);
    await tester.pumpWidget(OpenlogApp(store: MemoryTokenStore(), session: s));
    await tester.pump();

    expect(find.text('Connect to openlog'), findsOneWidget);
  });

  testWidgets('the alerts screen lists what is firing and offers to take it', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final a = ScriptedAlerts(
      incidents: [
        firing('i1'),
        firing('i2', state: 'acknowledged', ackBy: 'ada@example.com'),
      ],
      counts: const IncidentPageCounts(open: 1, acknowledged: 1, resolved: 0),
    );
    await tester.pumpWidget(signedInApp(s, alerts: a));
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('incident-i1')), findsOneWidget);
    expect(find.byKey(const Key('incident-i2')), findsOneWidget);
    // Only an open incident offers the button; an acknowledged one says who took it.
    expect(find.byKey(const Key('ack-i1')), findsOneWidget);
    expect(find.byKey(const Key('ack-i2')), findsNothing);
    expect(find.text('Acknowledged by ada@example.com'), findsOneWidget);

    await tester.tap(find.byKey(const Key('ack-i1')));
    await tester.pump();
    expect(a.calls, contains('acknowledge:i1'));
  });

  testWidgets('an incident row opens the incident, which asks the server', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final a = ScriptedAlerts(
      incidents: [firing('i1')],
      counts: const IncidentPageCounts(open: 1, acknowledged: 0, resolved: 0),
    );
    late ScriptedIncident detail;
    await tester.pumpWidget(
      signedInApp(
        s,
        alerts: a,
        incident: (id) => detail = ScriptedIncident(detailOf(id)),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('incident-i1')));
    await tester.pumpAndSettle();

    // The screen asked for its own copy rather than drawing the row again: the
    // timeline and the deliveries are only on the detail.
    expect(detail.calls, contains('refresh'));
    expect(detail.id, 'i1');
    expect(find.byKey(const Key('event-1')), findsOneWidget);
    expect(find.byKey(const Key('delivery-d1')), findsOneWidget);
    // A notification that failed is the difference between "nobody was told"
    // and "nobody looked", so the error has to be on screen.
    expect(find.text('smtp: connection refused'), findsOneWidget);
    expect(find.text('Failed'), findsOneWidget);
  });

  testWidgets('the incident leads to the service the rule was about', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final a = ScriptedAlerts(
      incidents: [firing('i1')],
      counts: const IncidentPageCounts(open: 1, acknowledged: 0, resolved: 0),
    );
    final names = <String>[];
    await tester.pumpWidget(
      signedInApp(
        s,
        alerts: a,
        incident: (id) => ScriptedIncident(detailOf(id)),
        serviceOverview: (name) {
          names.add(name);
          return ScriptedOverview(name, overviewOf());
        },
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('incident-i1')));
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('incident-open-service')));
    await tester.pumpAndSettle();

    // The whole point of the chain: the label on the incident chose the service.
    expect(names, ['checkout']);
    expect(find.text('Golden signals'), findsNothing); // no section heading
    expect(find.text('42.0 ms'), findsOneWidget); // p50 from the overview
    expect(find.text('12%'), findsOneWidget); // error rate, in red
  });

  testWidgets('an incident with no service label offers no way through', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final a = ScriptedAlerts(
      incidents: [firing('i1')],
      counts: const IncidentPageCounts(open: 1, acknowledged: 0, resolved: 0),
    );
    await tester.pumpWidget(
      signedInApp(
        s,
        alerts: a,
        // A host or an OQL rule has no service.name, and a dead button that
        // opens a screen about nothing would be worse than no button.
        incident: (id) =>
            ScriptedIncident(detailOf(id, labels: const {'host.id': 'h1'})),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('incident-i1')));
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('incident-open-service')), findsNothing);
    // The labels are still shown, because they are what the person filtered on.
    expect(find.text('host.id'), findsOneWidget);
  });

  testWidgets('resolving sends what was typed, and a 409 is said out loud', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final a = ScriptedAlerts(
      incidents: [firing('i1')],
      counts: const IncidentPageCounts(open: 1, acknowledged: 0, resolved: 0),
    );
    late ScriptedIncident detail;
    await tester.pumpWidget(
      signedInApp(
        s,
        alerts: a,
        incident: (id) =>
            detail = ScriptedIncident(detailOf(id), failOnResolve: true),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('incident-i1')));
    await tester.pumpAndSettle();

    await tester.enterText(
      find.byKey(const Key('incident-note')),
      'restarted the pool',
    );
    await tester.tap(find.byKey(const Key('incident-resolve')));
    await tester.pumpAndSettle();

    expect(detail.calls, contains('resolve:restarted the pool'));
    expect(
      find.text('That alert resolved before it could be acknowledged.'),
      findsOneWidget,
    );
  });

  testWidgets('a resolved incident is history, so it offers no buttons', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final a = ScriptedAlerts(
      incidents: [firing('i1')],
      counts: const IncidentPageCounts(open: 1, acknowledged: 0, resolved: 0),
    );
    await tester.pumpWidget(
      signedInApp(
        s,
        alerts: a,
        incident: (id) =>
            ScriptedIncident(detailOf(id, state: AlertIncidentState.resolved)),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('incident-i1')));
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('incident-resolve')), findsNothing);
    expect(find.byKey(const Key('incident-ack')), findsNothing);
    expect(find.byKey(const Key('incident-note')), findsNothing);
  });

  testWidgets(
    'a service that stopped serving says so rather than four zeroes',
    (tester) async {
      final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
      await tester.pumpWidget(
        signedInApp(
          s,
          services: ScriptedServices(services: [service('checkout')]),
          serviceOverview: (name) =>
              ScriptedOverview(name, overviewOf(requests: 0)),
        ),
      );
      await tester.pumpAndSettle();
      await goTo(tester, 'APM');
      await tester.tap(find.byKey(const Key('service-checkout')));
      await tester.pumpAndSettle();

      expect(
        find.text('This service has not reported in the window.'),
        findsOneWidget,
      );
    },
  );

  testWidgets('the error inbox is not asked for until the tab is opened', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    late ScriptedErrors errors;
    await tester.pumpWidget(
      signedInApp(
        s,
        services: ScriptedServices(services: [service('checkout')]),
        serviceOverview: (name) => ScriptedOverview(name, overviewOf()),
        serviceErrors: (name) =>
            errors = ScriptedErrors(name, inboxOf([errorGroup('g1')])),
      ),
    );
    await tester.pumpAndSettle();
    await goTo(tester, 'APM');
    await tester.tap(find.byKey(const Key('service-checkout')));
    await tester.pumpAndSettle();

    // TabBarView builds both pages; building is not looking.
    expect(errors.calls, isEmpty);

    await tester.tap(find.byKey(const Key('tab-errors')));
    await tester.pumpAndSettle();
    expect(errors.calls, ['refresh']);
    expect(find.byKey(const Key('error-g1')), findsOneWidget);
  });

  testWidgets('an error leads to the request it happened in', (tester) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final asked = <String>[];
    await tester.pumpWidget(
      signedInApp(
        s,
        services: ScriptedServices(services: [service('checkout')]),
        serviceOverview: (name) => ScriptedOverview(name, overviewOf()),
        serviceErrors: (name) =>
            ScriptedErrors(name, inboxOf([errorGroup('g1')])),
        trace: (id) {
          asked.add(id);
          return ScriptedTrace(id, traceOf());
        },
      ),
    );
    await tester.pumpAndSettle();
    await goTo(tester, 'APM');
    await tester.tap(find.byKey(const Key('service-checkout')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('tab-errors')));
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('error-g1')));
    await tester.pumpAndSettle();

    // The last link: the group carried the trace id, so the chain that began
    // with an alert ends at the request itself.
    expect(asked, ['abcdef']);
    expect(find.byKey(const Key('span-root')), findsOneWidget);
    expect(find.text('upstream timed out'), findsWidgets);
  });

  testWidgets(
    'an error whose sample aged out says so instead of going nowhere',
    (tester) async {
      final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
      await tester.pumpWidget(
        signedInApp(
          s,
          services: ScriptedServices(services: [service('checkout')]),
          serviceOverview: (name) => ScriptedOverview(name, overviewOf()),
          serviceErrors: (name) =>
              ScriptedErrors(name, inboxOf([errorGroup('g1', traceId: '')])),
        ),
      );
      await tester.pumpAndSettle();
      await goTo(tester, 'APM');
      await tester.tap(find.byKey(const Key('service-checkout')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('tab-errors')));
      await tester.pumpAndSettle();

      expect(find.text('No trace was kept for this error.'), findsOneWidget);
      await tester.tap(find.byKey(const Key('error-g1')));
      await tester.pumpAndSettle();
      // Still on the service: a dead tap must not look like a failed navigation.
      expect(find.byKey(const Key('tab-errors')), findsOneWidget);
    },
  );

  testWidgets('nothing firing reads as good news, not as an empty page', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final a = ScriptedAlerts(
      counts: const IncidentPageCounts(open: 0, acknowledged: 0, resolved: 11),
    );
    await tester.pumpWidget(signedInApp(s, alerts: a));
    await tester.pumpAndSettle();

    expect(find.text('Nothing is firing.'), findsOneWidget);
    // "Nothing is firing" reads differently when eleven things fired and recovered today.
    expect(find.text('11 resolved in the last 7 days'), findsOneWidget);
  });

  testWidgets(
    'a role that may not read alerts is told so, not shown an empty list',
    (tester) async {
      final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
      final a = ScriptedAlerts()
        ..failure = const SessionFailure('alertsForbidden', '');
      await tester.pumpWidget(signedInApp(s, alerts: a));
      await tester.pumpAndSettle();

      expect(
        find.text('Your role does not allow reading alerts.'),
        findsOneWidget,
      );
    },
  );

  testWidgets('the alerts screen asks the server as soon as it is shown', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final a = ScriptedAlerts();
    await tester.pumpWidget(signedInApp(s, alerts: a));
    await tester.pumpAndSettle();

    expect(a.calls, contains('refresh'));
  });

  testWidgets(
    'the sections are reached from the drawer, and each keeps its state',
    (tester) async {
      final session = ScriptedSession(stage: SessionStage.signedIn)..me = me();
      final services = ScriptedServices(services: [service('checkout')]);
      final logs = ScriptedLogs(logs: [logLine('connection refused')]);
      await tester.pumpWidget(
        signedInApp(
          session,
          alerts: ScriptedAlerts(incidents: [firing('i1')]),
          services: services,
          logs: logs,
        ),
      );
      await tester.pumpAndSettle();

      expect(find.byKey(const Key('incident-i1')), findsOneWidget);

      await goTo(tester, 'APM');
      expect(find.byKey(const Key('service-checkout')), findsOneWidget);

      await goTo(tester, 'Logs');
      expect(find.text('connection refused'), findsOneWidget);

      // Back to the first tab: an IndexedStack keeps it built, so the list is
      // still there and was not reloaded, which on an on-call screen is the
      // difference between checking two things and losing one.
      await goTo(tester, 'Alerts');
      expect(find.byKey(const Key('incident-i1')), findsOneWidget);
      expect(services.calls.where((c) => c.startsWith('refresh')).length, 1);
    },
  );

  testWidgets(
    'a service search asks the server rather than filtering locally',
    (tester) async {
      final session = ScriptedSession(stage: SessionStage.signedIn)..me = me();
      final services = ScriptedServices(services: [service('checkout')]);
      await tester.pumpWidget(signedInApp(session, services: services));
      await tester.pumpAndSettle();
      await goTo(tester, 'APM');

      await tester.enterText(find.byKey(const Key('services-search')), 'pay');
      await tester.testTextInput.receiveAction(TextInputAction.search);
      await tester.pumpAndSettle();

      // Searching the rows already loaded would hide every service that did not
      // fit in the first page.
      expect(services.calls, contains('refresh:pay'));
    },
  );

  testWidgets('the log severity filter is a query, not a client-side sieve', (
    tester,
  ) async {
    final session = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final logs = ScriptedLogs(logs: [logLine('boom')]);
    await tester.pumpWidget(signedInApp(session, logs: logs));
    await tester.pumpAndSettle();
    await goTo(tester, 'Logs');

    // Scoped to the filter: a log row shows its own severity as "ERROR" too,
    // so a bare text finder taps the list instead of the button.
    await tester.tap(
      find.descendant(
        of: find.byKey(const Key('logs-severity')),
        matching: find.text('ERROR'),
      ),
    );
    await tester.pumpAndSettle();

    expect(logs.calls, contains('refresh::ERROR'));
  });

  testWidgets('a service with no errors is not coloured as if it had some', (
    tester,
  ) async {
    final session = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    await tester.pumpWidget(
      signedInApp(
        session,
        services: ScriptedServices(
          services: [service('quiet'), service('broken', errorRate: 0.12)],
        ),
      ),
    );
    await tester.pumpAndSettle();
    await goTo(tester, 'APM');

    expect(find.text('0.0%'), findsOneWidget);
    expect(find.text('12%'), findsOneWidget);
  });

  testWidgets('dashboards are a section that lists what can be opened', (
    tester,
  ) async {
    final session = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final boards = ScriptedDashboards(
      dashboards: [board('d1', 'Checkout health')],
    );
    await tester.pumpWidget(signedInApp(session, dashboards: boards));
    await tester.pumpAndSettle();

    await goTo(tester, 'Dashboards');

    expect(find.byKey(const Key('dashboard-d1')), findsOneWidget);
    expect(find.text('Checkout health'), findsOneWidget);
    expect(find.text('4 widgets on 1 pages'), findsOneWidget);
    expect(boards.calls, contains('refresh:'));
  });

  testWidgets('a section is not asked about until it is looked at', (
    tester,
  ) async {
    final session = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final hosts = ScriptedHosts();
    await tester.pumpWidget(signedInApp(session, hosts: hosts));
    await tester.pumpAndSettle();

    // Thirteen sections are built by the IndexedStack; asking the server
    // thirteen questions for screens nobody has opened is the part that costs.
    expect(hosts.calls, isEmpty);

    await goTo(tester, 'Hosts');
    expect(hosts.calls, ['refresh']);

    // And not again on the way back.
    await goTo(tester, 'Alerts');
    await goTo(tester, 'Hosts');
    expect(hosts.calls, ['refresh']);
  });

  testWidgets('the console runs what was typed and shows what came back', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final q = ScriptedQuery(answer: singleAnswer(42));
    await tester.pumpWidget(signedInApp(s, query: q));
    await tester.pumpAndSettle();
    await goTo(tester, 'Query');

    // Nothing is asked until the person asks: a console with no query has no
    // question to put to the server.
    expect(q.calls, isEmpty);
    expect(find.text('Write a query and press Run.'), findsOneWidget);

    await tester.enterText(
      find.byKey(const Key('query-text')),
      'SELECT count(*) FROM logs',
    );
    await tester.tap(find.byKey(const Key('query-run')));
    await tester.pumpAndSettle();

    expect(q.calls, ['SELECT count(*) FROM logs']);
    expect(find.byKey(const Key('single-query')), findsOneWidget);
    expect(find.text('42'), findsOneWidget);
    expect(find.text('1200 rows read in 34 ms'), findsOneWidget);
  });

  testWidgets('a rejected query shows the server reason, not a shrug', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final q = ScriptedQuery(rejectWith: 'unexpected token at position 4');
    await tester.pumpWidget(signedInApp(s, query: q));
    await tester.pumpAndSettle();
    await goTo(tester, 'Query');

    await tester.enterText(find.byKey(const Key('query-text')), 'bad');
    await tester.tap(find.byKey(const Key('query-run')));
    await tester.pumpAndSettle();

    expect(
      find.text(
        'The server would not run that: unexpected token at position 4',
      ),
      findsOneWidget,
    );
  });

  testWidgets('a recent query goes back in the box, it does not re-run', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final q = ScriptedQuery(answer: singleAnswer(42));
    await tester.pumpWidget(signedInApp(s, query: q));
    await tester.pumpAndSettle();
    await goTo(tester, 'Query');

    await tester.enterText(find.byKey(const Key('query-text')), 'SELECT 1');
    await tester.tap(find.byKey(const Key('query-run')));
    await tester.pumpAndSettle();
    await tester.enterText(find.byKey(const Key('query-text')), '');
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('history-0')));
    await tester.pumpAndSettle();

    // Back in the box and not run again: a query that was expensive once
    // should not run because a thumb brushed the list.
    expect(q.calls, ['SELECT 1']);
    expect(
      tester
          .widget<TextField>(find.byKey(const Key('query-text')))
          .controller!
          .text,
      'SELECT 1',
    );
  });

  testWidgets('a trace row opens the request it stands for', (tester) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final asked = <String>[];
    await tester.pumpWidget(
      signedInApp(
        s,
        traces: ScriptedTraces([spanRow('t1'), spanRow('t2', error: true)]),
        trace: (id) {
          asked.add(id);
          return ScriptedTrace(id, traceOf());
        },
      ),
    );
    await tester.pumpAndSettle();
    await goTo(tester, 'Traces');

    expect(find.byKey(const Key('trace-t1')), findsOneWidget);
    await tester.tap(find.byKey(const Key('trace-t2')));
    await tester.pumpAndSettle();

    expect(asked, ['t2']);
    expect(find.byKey(const Key('span-root')), findsOneWidget);
  });

  testWidgets('the slowest switch is a new question, not a local sort', (
    tester,
  ) async {
    final s = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    final t = ScriptedTraces([spanRow('t1')]);
    await tester.pumpWidget(signedInApp(s, traces: t));
    await tester.pumpAndSettle();
    await goTo(tester, 'Traces');

    await tester.tap(find.text('Slowest'));
    await tester.pumpAndSettle();

    // The server decides which are slowest: this page holds fifty rows out of
    // however many there were, so sorting them here would rank the wrong set.
    expect(t.calls, ['refresh::true']);
    expect(t.slowest, isTrue);
  });

  testWidgets('each drawer entry opens the section it names', (tester) async {
    final session = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    await tester.pumpWidget(signedInApp(session));
    await tester.pumpAndSettle();

    // The drawer, the titles and the bodies are three lists that have to stay
    // in step. They did not: inserting a section in the middle used to move
    // every body after it off its own index, which does not crash -- it just
    // shows one section under another's name. Each entry below is checked by
    // something only that section has.
    const markers = <String, Key>{
      'Hosts': Key('hosts-search'),
      'Containers': Key('containers-search'),
      'Costs': Key('costs-body'),
      'Kubernetes': Key('pods-search'),
      'APM': Key('services-search'),
      'Browser': Key('rum-body'),
      'Databases': Key('databases-search'),
      'SLOs': Key('slos-search'),
      'Synthetics': Key('synthetics-search'),
      'Job monitoring': Key('jobs-search'),
      'Vulnerabilities': Key('vulnerabilities-search'),
      'Logs': Key('logs-search'),
      'Traces': Key('traces-search'),
      'Metrics': Key('metrics-search'),
      'Query': Key('query-text'),
      'Dashboards': Key('dashboards-search'),
      'Inventory search': Key('inventory-category'),
      'Alerts': Key('alerts-body'),
      'Settings': Key('signed-in-as'),
    };

    for (final entry in markers.entries) {
      await goTo(tester, entry.key);
      expect(
        find.descendant(
          of: find.byType(AppBar),
          matching: find.text(entry.key),
        ),
        findsOneWidget,
        reason: 'the app bar does not say ${entry.key}',
      );
      expect(
        find.byKey(entry.value),
        findsOneWidget,
        reason: '${entry.key} does not show its own body',
      );
    }
  });

  testWidgets('the app opens on what is firing', (tester) async {
    final session = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    await tester.pumpWidget(
      signedInApp(
        session,
        alerts: ScriptedAlerts(
          incidents: [firing('i1')],
          counts: const IncidentPageCounts(
            open: 1,
            acknowledged: 0,
            resolved: 0,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    // Not a number written next to the tab list: that number was wrong twice.
    expect(find.byKey(const Key('incident-i1')), findsOneWidget);
  });

  testWidgets('the drawer lists every section the app has, in the web order', (
    tester,
  ) async {
    // Tall enough for every entry to be built at once: this reads the drawn
    // order, so an entry that is merely off-screen would look like one that is
    // in the wrong place.
    await tester.binding.setSurfaceSize(const Size(420, 1600));
    addTearDown(() => tester.binding.setSurfaceSize(null));

    final session = ScriptedSession(stage: SessionStage.signedIn)..me = me();
    await tester.pumpWidget(signedInApp(session));
    await tester.pumpAndSettle();
    await openDrawer(tester);

    // The order as it is drawn, not "is each of these somewhere": the list
    // mirrors web/src/components/AppShell.tsx, and an entry inserted in the
    // wrong place is exactly the mistake this is here to catch. It also keeps
    // the drawer honest about the tab indices behind it -- the shell's
    // IndexedStack is this list, in this order.
    final drawn = <String>[
      for (final e in find.byType(InkWell).evaluate())
        if ((e.widget.key as ValueKey<String>?)?.value case final String k
            when k.startsWith('nav-'))
          k.substring(4),
    ];

    expect(drawn, const [
      'Hosts',
      'Containers',
      'Costs',
      'Kubernetes',
      'APM',
      'Browser',
      'Databases',
      'SLOs',
      'Synthetics',
      'Job monitoring',
      'Vulnerabilities',
      'Logs',
      'Traces',
      'Metrics',
      'Query',
      'Dashboards',
      'Inventory search',
      'Alerts',
      'Settings',
    ]);
  });
}
