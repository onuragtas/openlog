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
import 'package:openlog_mobile/src/logs.dart';
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

/// The signed-in app with all three lists scripted, which is what the shell
/// needs: it builds every tab at once so moving between them keeps their state.
Widget signedInApp(
  SessionController session, {
  AlertsController? alerts,
  ServicesController? services,
  LogsController? logs,
}) => OpenlogApp(
  store: MemoryTokenStore(),
  session: session,
  alerts: alerts ?? ScriptedAlerts(),
  services: services ?? ScriptedServices(),
  logs: logs ?? ScriptedLogs(),
);

/// Opens the account drawer, which is where the signed-in details moved when
/// the alerts list took over the screen.
Future<void> openDrawer(WidgetTester tester) async {
  tester.state<ScaffoldState>(find.byType(Scaffold).last).openDrawer();
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
      await openDrawer(tester);

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
    await openDrawer(tester);

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
    'the three lists are tabs, and moving between them keeps each one',
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

      await tester.tap(find.byKey(const Key('tab-services')));
      await tester.pumpAndSettle();
      expect(find.byKey(const Key('service-checkout')), findsOneWidget);

      await tester.tap(find.byKey(const Key('tab-logs')));
      await tester.pumpAndSettle();
      expect(find.text('connection refused'), findsOneWidget);

      // Back to the first tab: an IndexedStack keeps it built, so the list is
      // still there and was not reloaded, which on an on-call screen is the
      // difference between checking two things and losing one.
      await tester.tap(find.byKey(const Key('tab-alerts')));
      await tester.pumpAndSettle();
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
      await tester.tap(find.byKey(const Key('tab-services')));
      await tester.pumpAndSettle();

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
    await tester.tap(find.byKey(const Key('tab-logs')));
    await tester.pumpAndSettle();

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
    await tester.tap(find.byKey(const Key('tab-services')));
    await tester.pumpAndSettle();

    expect(find.text('0.0%'), findsOneWidget);
    expect(find.text('12%'), findsOneWidget);
  });
}
