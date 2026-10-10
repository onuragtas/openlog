// The sections that are a list of things with a search box.
//
// One file, because they are one shape: fetch a page, keep the rows, let
// ListController handle what happens when the request fails. A section that
// needs more than that -- alerts can be acknowledged, dashboards run queries --
// has a file of its own.
import 'package:flutter/foundation.dart';

import 'alerts.dart';
import 'account.dart';
import 'api/client.dart';
import 'api/schema.g.dart';
import 'dashboards.dart';
import 'detail.dart';
import 'agents.dart';
import 'discovery.dart';
import 'errors.dart';
import 'keys.dart';
import 'list_controller.dart';
import 'logs.dart';
import 'members.dart';
import 'session.dart';
import 'query.dart';
import 'sampling.dart';
import 'templates.dart';
import 'services.dart';

/// Every section's controller, for one signed-in client.
///
/// One object rather than thirteen parameters: the shell needs all of them,
/// the app has to dispose all of them, and a list that long threaded through
/// two widgets is a list that will one day be missing an entry.
class Sections {
  Sections({
    required OpenlogClient client,
    HostsController? hosts,
    ContainersController? containers,
    PodsController? pods,
    ServicesController? services,
    DatabasesController? databases,
    SlosController? slos,
    SyntheticsController? synthetics,
    JobsController? jobs,
    VulnerabilitiesController? vulnerabilities,
    LogsController? logs,
    TracesController? traces,
    MetricsController? metrics,
    RumController? rum,
    CostsController? costs,
    InventoryController? inventory,
    FleetController? fleet,
    IntegrationsController? integrations,
    ProfilesController? profiles,
    OnboardingController? onboarding,
    SessionsController? sessions,
    AccountController? account,
    MembersController? members,
    LicenseKeysController? licenseKeys,
    ApiKeysController? apiKeys,
    BrowserKeysController? browserKeys,
    LogsController Function({
      String traceId,
      String podUid,
      String containerId,
    })?
    scopedLogs,
    DashboardsController? dashboards,
    AlertsController? alerts,
    AlertRulesController? rules,
    AlertChannelsController? channels,
    AlertMutesController? mutes,
    AlertRoutesController? routes,
    AlertCalendarsController? calendars,
    AlertDeliveriesController Function(String channelId)? deliveries,
    ApmAgentsController? agents,
    SamplingController Function()? sampling,
    ErrorInboxController? errors,
    ErrorGroupController Function(ApmErrorGroup group)? errorGroup,
    TemplatesController? templates,
    TemplateSetupController Function(AlertTemplate template, String language)?
    templateSetup,
    QueryController? query,
    IncidentController Function(String id)? incident,
    ServiceOverviewController Function(String serviceName)? serviceOverview,
    ServiceErrorsController Function(String serviceName)? serviceErrors,
    ServiceTracesController Function(String serviceName)? serviceTraces,
    ServiceMapController Function(String serviceName)? serviceMap,
    ServiceTransactionsController Function(String serviceName)?
    serviceTransactions,
    ServiceDatabasesController Function(String serviceName)? serviceDatabases,
    ServiceDeploymentsController Function(String serviceName)?
    serviceDeployments,
    ServiceAboutController Function(String serviceName)? serviceAbout,
    TraceController Function(String traceId)? trace,
    MetricController Function(String name)? metric,
    RumOverviewController Function(String app)? rumOverview,
    HostController Function(String hostId)? host,
    ContainerController Function(String containerId)? container,
    PodController Function(String podUid)? pod,
    ProfileFunctionsController Function({
      required String service,
      required String type,
      required String environment,
    })?
    profileFunctions,
  }) : incident = incident ?? ((id) => IncidentController(client, id)),
       serviceOverview =
           serviceOverview ??
           ((name) => ServiceOverviewController(client, name)),
       serviceErrors =
           serviceErrors ?? ((name) => ServiceErrorsController(client, name)),
       serviceTraces =
           serviceTraces ?? ((name) => ServiceTracesController(client, name)),
       serviceMap =
           serviceMap ?? ((name) => ServiceMapController(client, name)),
       serviceTransactions =
           serviceTransactions ??
           ((name) => ServiceTransactionsController(client, name)),
       serviceDatabases =
           serviceDatabases ??
           ((name) => ServiceDatabasesController(client, name)),
       serviceDeployments =
           serviceDeployments ??
           ((name) => ServiceDeploymentsController(client, name)),
       serviceAbout =
           serviceAbout ?? ((name) => ServiceAboutController(client, name)),
       trace = trace ?? ((id) => TraceController(client, id)),
       metric = metric ?? ((name) => MetricController(client, name)),
       rumOverview =
           rumOverview ?? ((app) => RumOverviewController(client, app)),
       host = host ?? ((id) => HostController(client, id)),
       container = container ?? ((id) => ContainerController(client, id)),
       pod = pod ?? ((uid) => PodController(client, uid)),
       profileFunctions =
           profileFunctions ??
           (({
             required String service,
             required String type,
             required String environment,
           }) => ProfileFunctionsController(
             client,
             service: service,
             type: type,
             environment: environment,
           )),
       hosts = hosts ?? HostsController(client),
       containers = containers ?? ContainersController(client),
       pods = pods ?? PodsController(client),
       services = services ?? ServicesController(client),
       databases = databases ?? DatabasesController(client),
       slos = slos ?? SlosController(client),
       synthetics = synthetics ?? SyntheticsController(client),
       jobs = jobs ?? JobsController(client),
       vulnerabilities = vulnerabilities ?? VulnerabilitiesController(client),
       logs = logs ?? LogsController(client),
       traces = traces ?? TracesController(client),
       metrics = metrics ?? MetricsController(client),
       rum = rum ?? RumController(client),
       costs = costs ?? CostsController(client),
       inventory = inventory ?? InventoryController(client),
       fleet = fleet ?? FleetController(client),
       integrations = integrations ?? IntegrationsController(client),
       profiles = profiles ?? ProfilesController(client),
       onboarding = onboarding ?? OnboardingController(client),
       sessions = sessions ?? SessionsController(client),
       account = account ?? AccountController(client),
       members = members ?? MembersController(client),
       licenseKeys = licenseKeys ?? LicenseKeysController(client),
       apiKeys = apiKeys ?? ApiKeysController(client),
       browserKeys = browserKeys ?? BrowserKeysController(client),
       scopedLogs =
           scopedLogs ??
           (({
             String traceId = '',
             String podUid = '',
             String containerId = '',
           }) => LogsController(
             client,
             traceId: traceId,
             podUid: podUid,
             containerId: containerId,
           )),
       dashboards = dashboards ?? DashboardsController(client),
       query = query ?? QueryController(client),
       rules = rules ?? AlertRulesController(client),
       channels = channels ?? AlertChannelsController(client),
       mutes = mutes ?? AlertMutesController(client),
       routes = routes ?? AlertRoutesController(client),
       calendars = calendars ?? AlertCalendarsController(client),
       deliveries =
           deliveries ??
           ((channelId) =>
               AlertDeliveriesController(client, channelId: channelId)),
       agents = agents ?? ApmAgentsController(client),
       sampling = sampling ?? (() => SamplingController(client)),
       errors = errors ?? ErrorInboxController(client),
       errorGroup =
           errorGroup ?? ((group) => ErrorGroupController(client, group)),
       templates = templates ?? TemplatesController(client),
       templateSetup =
           templateSetup ??
           ((template, language) =>
               TemplateSetupController(client, template, language: language)),
       alerts = alerts ?? AlertsController(client);

  final HostsController hosts;
  final ContainersController containers;
  final PodsController pods;
  final ServicesController services;
  final DatabasesController databases;
  final SlosController slos;
  final SyntheticsController synthetics;
  final JobsController jobs;
  final VulnerabilitiesController vulnerabilities;
  final LogsController logs;
  final TracesController traces;
  final MetricsController metrics;
  final RumController rum;
  final CostsController costs;
  final InventoryController inventory;
  final FleetController fleet;
  final IntegrationsController integrations;
  final ProfilesController profiles;
  final OnboardingController onboarding;
  final SessionsController sessions;

  /// The account behind the token: its password and its language.
  final AccountController account;

  /// Who is in the organization, and who has been asked to join.
  final MembersController members;

  /// The three kinds of key the settings tabs manage.
  final LicenseKeysController licenseKeys;
  final ApiKeysController apiKeys;
  final BrowserKeysController browserKeys;

  /// The logs of one request, pod or container. A controller per screen,
  /// disposed with it, because each one answers about a different thing.
  final LogsController Function({
    String traceId,
    String podUid,
    String containerId,
  })
  scopedLogs;
  final DashboardsController dashboards;
  final QueryController query;
  final AlertRulesController rules;
  final AlertChannelsController channels;
  final AlertMutesController mutes;
  final AlertRoutesController routes;

  /// Reached from the mutes screen, which is the only thing that uses them.
  final AlertCalendarsController calendars;

  /// The delivery log, for one channel or for all of them. A controller per
  /// screen, like the detail ones: which channel it is about is fixed when
  /// the screen opens.
  final AlertDeliveriesController Function(String channelId) deliveries;

  /// Which language agent each service runs, reached from the services
  /// list as on the web.
  final ApmAgentsController agents;

  /// The tail sampling policy. A controller per screen: it holds a draft
  /// somebody is editing, which has no business outliving the screen.
  final SamplingController Function() sampling;

  /// The error inbox of every service, and one group's comments and
  /// actions. The group controller is per screen: it holds the comments of
  /// the group that was opened.
  final ErrorInboxController errors;
  final ErrorGroupController Function(ApmErrorGroup group) errorGroup;

  /// The template catalog, and one template being set up. The setup is a
  /// controller per screen: it holds a rendered rule and its preview, which
  /// belong to that one visit.
  final TemplatesController templates;
  final TemplateSetupController Function(
    AlertTemplate template,
    String language,
  )
  templateSetup;
  final AlertsController alerts;

  /// Detail screens get a controller each, made when the screen opens and
  /// disposed with it -- unlike the sections, whose rows are worth keeping while
  /// the person moves between tabs. Functions rather than instances so a test
  /// can hand a screen a scripted one without a server.
  final IncidentController Function(String id) incident;
  final ServiceOverviewController Function(String serviceName) serviceOverview;
  final ServiceErrorsController Function(String serviceName) serviceErrors;
  final ServiceTracesController Function(String serviceName) serviceTraces;
  final ServiceMapController Function(String serviceName) serviceMap;
  final ServiceTransactionsController Function(String serviceName)
  serviceTransactions;
  final ServiceDatabasesController Function(String serviceName)
  serviceDatabases;
  final ServiceDeploymentsController Function(String serviceName)
  serviceDeployments;
  final ServiceAboutController Function(String serviceName) serviceAbout;
  final TraceController Function(String traceId) trace;
  final MetricController Function(String name) metric;
  final RumOverviewController Function(String app) rumOverview;
  final HostController Function(String hostId) host;
  final ContainerController Function(String containerId) container;
  final PodController Function(String podUid) pod;
  final ProfileFunctionsController Function({
    required String service,
    required String type,
    required String environment,
  })
  profileFunctions;

  /// Everything that has to be disposed, in the order the drawer lists them,
  /// which is the web's order. Calendars are in here too although the drawer
  /// does not list them: they are reached from the mutes screen, and a
  /// controller left out of this list is a controller never disposed.
  List<ChangeNotifier> get all => [
    onboarding,
    sessions,
    account,
    members,
    licenseKeys,
    apiKeys,
    browserKeys,
    hosts,
    containers,
    costs,
    pods,
    integrations,
    services,
    agents,
    errors,
    rum,
    profiles,
    databases,
    slos,
    synthetics,
    jobs,
    vulnerabilities,
    logs,
    traces,
    metrics,
    query,
    dashboards,
    inventory,
    fleet,
    rules,
    channels,
    mutes,
    calendars,
    routes,
    templates,
    alerts,
  ];

  void dispose() {
    for (final c in all) {
      c.dispose();
    }
  }
}

/// Everything these sections share: a client, a search box, and a fetch.
abstract class SectionController<T> extends ListController<T> {
  SectionController(this.client);

  final OpenlogClient client;

  /// What is in the search box. Sent to the server rather than filtered here,
  /// so a search is not limited to the rows that fit in the first page.
  String query = '';

  @override
  String get forbiddenKind => 'sectionForbidden';
}

class HostsController extends SectionController<Host> {
  HostsController(super.client);

  @override
  Future<List<Host>> fetch() async => (await client.hosts(q: query)).hosts;
}

class ContainersController extends SectionController<ApiContainer> {
  ContainersController(super.client);

  @override
  Future<List<ApiContainer>> fetch() async =>
      (await client.containers(q: query)).containers;
}

class PodsController extends SectionController<KubernetesPod> {
  PodsController(super.client);

  @override
  Future<List<KubernetesPod>> fetch() async =>
      (await client.pods(q: query)).pods;
}

class SlosController extends SectionController<SloListItem> {
  SlosController(super.client);

  @override
  Future<List<SloListItem>> fetch() async {
    final slos = (await client.slos(q: query)).slos;
    // The ones in trouble first. An SLO list read top-down on a phone has to
    // start with the budget that is nearly gone, not with the alphabet.
    return [...slos]..sort((a, b) {
      final ar = a.status?.budget.remainingRatio ?? 2;
      final br = b.status?.budget.remainingRatio ?? 2;
      return ar.compareTo(br);
    });
  }
}

class SyntheticsController extends SectionController<SyntheticCheckListItem> {
  SyntheticsController(super.client);

  @override
  Future<List<SyntheticCheckListItem>> fetch() async =>
      (await client.synthetics(q: query)).checks;
}

class JobsController extends SectionController<JobMonitor> {
  JobsController(super.client);

  @override
  Future<List<JobMonitor>> fetch() async =>
      (await client.jobMonitors(q: query)).monitors;
}

class VulnerabilitiesController extends SectionController<VulnGroup> {
  VulnerabilitiesController(super.client);

  @override
  Future<List<VulnGroup>> fetch() async =>
      (await client.vulnerabilities(q: query)).vulnerabilities;
}

class DatabasesController extends SectionController<DbInstance> {
  DatabasesController(super.client);

  @override
  Future<List<DbInstance>> fetch() async =>
      (await client.dbInstances(q: query)).instances;
}

/// Entry spans: the traces section, which is a list of requests.
class TracesController extends SectionController<SpanQueryRow> {
  TracesController(super.client);

  /// Slowest instead of newest. The two questions a traces list answers are
  /// "what just happened" and "what is slow", and on a phone a switch between
  /// them beats the web's sort menu.
  bool slowest = false;

  @override
  Future<List<SpanQueryRow>> fetch() async =>
      (await client.traces(q: query.trim(), slowest: slowest)).rows;
}

/// Metric names, for the metrics explorer.
class MetricsController extends SectionController<MetricInfo> {
  MetricsController(super.client);

  @override
  Future<List<MetricInfo>> fetch() async =>
      (await client.metrics(q: query.trim())).metrics;
}

/// Browser applications, for the RUM section.
class RumController extends SectionController<RumApp> {
  RumController(super.client);

  @override
  Future<List<RumApp>> fetch() async {
    final apps = [...(await client.rumApps()).apps];
    // Busiest first: the application with the page views is the one whose
    // vitals anybody is going to look at.
    apps.sort((a, b) => b.views.compareTo(a.views));
    return apps;
  }
}

/// Inventory search: one category at a time, across every host.
class InventoryController extends SectionController<InventorySearchItem> {
  InventoryController(super.client);

  /// Packages to begin with: "which hosts have openssl" is the question this
  /// screen exists for, and it is asked about packages far more than about
  /// mounts.
  String category = 'package';

  @override
  Future<List<InventorySearchItem>> fetch() async =>
      (await client.inventory(category: category, q: query.trim())).items;
}

/// The agent fleet, read-only: how far behind it is and which host is where.
///
/// Two requests in one fetch, so the summary on top and the list under it
/// always describe the same moment -- separate controllers would let the
/// header say "3 outdated" over a list that has already moved on.
///
/// Read-only on purpose. Changing a rollout policy is a fleet-wide action
/// with a blast radius, and a phone in a pocket is the wrong place for the
/// button that starts one.
class FleetController extends SectionController<FleetHost> {
  FleetController(super.client);

  FleetSummary? summary;

  @override
  Future<List<FleetHost>> fetch() async {
    summary = await client.fleetSummary();
    return (await client.fleetHosts(q: query.trim())).hosts;
  }
}

/// Integrations, read from the agents' own discovery.
///
/// The same inventory search the web's Integrations page is built on: the
/// `discovered_service` body carries whether the integration is collecting,
/// so neither side has to derive a status and the two cannot disagree.
class IntegrationsController extends SectionController<IntegrationInstance> {
  IntegrationsController(super.client);

  @override
  Future<List<IntegrationInstance>> fetch() async {
    final page = await client.inventory(
      category: 'discovered_service',
      q: query.trim(),
      limit: 200,
    );
    return [
      for (final item in page.items)
        ?instanceOf(
          hostId: item.hostId,
          hostName: item.hostName ?? item.hostId,
          key: item.key,
          data: item.data,
        ),
    ]..sort(compareInstances);
  }

  /// How many of each status, for the header -- the same counts the web puts
  /// above its table.
  ({int enabled, int needsConfiguration, int error, int notAvailable})
  get counts {
    var enabled = 0, needs = 0, error = 0, notAvailable = 0;
    for (final i in items) {
      switch (i.status) {
        case DiscoveredServiceIntegrationStatus.enabled:
          enabled++;
        case DiscoveredServiceIntegrationStatus.needsConfiguration:
          needs++;
        case DiscoveredServiceIntegrationStatus.error:
          error++;
        default:
          notAvailable++;
      }
    }
    return (
      enabled: enabled,
      needsConfiguration: needs,
      error: error,
      notAvailable: notAvailable,
    );
  }
}

/// What has been profiled, one row per service, environment and type.
class ProfilesController extends SectionController<ProfileService> {
  ProfilesController(super.client);

  @override
  Future<List<ProfileService>> fetch() async {
    final all = [...(await client.profileServices()).services];
    // Where the samples are: a profile with four samples cannot say anything,
    // and on a phone the first screenful has to be the one worth opening.
    all.sort((a, b) => b.samples.compareTo(a.samples));
    // Nothing is filtered here -- the server takes no query for this list, so
    // the search box filters what arrived rather than pretending to ask.
    final q = query.trim().toLowerCase();
    if (q.isEmpty) return all;
    return [
      for (final p in all)
        if (p.service.toLowerCase().contains(q) ||
            p.type.toLowerCase().contains(q))
          p,
    ];
  }
}

/// The alert rules, and the one write an on-call person actually makes from a
/// phone: turning a noisy rule off.
///
/// Everything else about a rule -- its condition, its thresholds, its
/// channels -- is a form, and a form with a threshold in it is not something
/// to fill in on a phone at three in the morning. Those stay on the web.
class AlertRulesController extends SectionController<AlertRule> {
  AlertRulesController(super.client);

  /// Which rule is being switched, so only that row is busy.
  String? busy;

  @override
  String get forbiddenKind => 'alertsForbidden';

  @override
  Future<List<AlertRule>> fetch() async {
    final rules = [...(await client.alertRules()).rules];
    final q = query.trim().toLowerCase();
    final matched = q.isEmpty
        ? rules
        : [
            for (final r in rules)
              if (r.name.toLowerCase().contains(q) ||
                  r.description.toLowerCase().contains(q))
                r,
          ];
    // Firing first, then the ones that are merely on, then the disabled: the
    // list is read from the top and what is paging someone belongs there.
    int rank(AlertRule r) => switch (r.status.state) {
      AlertRuleStatusState.firing => 0,
      AlertRuleStatusState.error => 1,
      AlertRuleStatusState.pending => 2,
      AlertRuleStatusState.disabled => 4,
      _ => 3,
    };
    matched.sort((a, b) {
      final byState = rank(a) - rank(b);
      return byState != 0 ? byState : a.name.compareTo(b.name);
    });
    return matched;
  }

  /// Turns [id] on or off, then reloads so the row shows the server's answer
  /// rather than this app's guess at it.
  Future<void> setEnabled(String id, {required bool enabled}) async {
    busy = id;
    failure = null;
    notifyListeners();
    try {
      await client.setAlertRuleEnabled(id, enabled: enabled);
      await refresh();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = e.status == 403
          ? const SessionFailure('alertsForbidden', '')
          : SessionFailure('unexpected', e.message);
    } finally {
      busy = null;
      notifyListeners();
    }
  }
}

/// The notification channels, and the one thing worth doing to them from a
/// phone: finding out whether they still reach anyone.
///
/// Creating and editing a channel means typing a webhook URL or an SMTP
/// password, which is neither pleasant nor wise on a phone; those stay on the
/// web. Testing one before a shift is exactly a phone job.
class AlertChannelsController extends SectionController<AlertChannel> {
  AlertChannelsController(super.client);

  /// False when OPENLOG_SECRETS_KEY is not set: the installation cannot store
  /// channel secrets at all, so an empty list means "not possible here"
  /// rather than "nobody made one".
  bool secretsConfigured = true;

  /// Which channel is being tested, so only that row is busy.
  String? testing;

  /// What each test said, kept by channel id. Not cleared on refresh: the
  /// answer to "did this work" should survive the list reloading under it.
  final results = <String, AlertChannelTestResult>{};

  @override
  String get forbiddenKind => 'alertsForbidden';

  @override
  Future<List<AlertChannel>> fetch() async {
    final page = await client.alertChannels();
    secretsConfigured = page.secretsConfigured;
    final q = query.trim().toLowerCase();
    return [
      for (final c in page.channels)
        if (q.isEmpty ||
            c.name.toLowerCase().contains(q) ||
            c.type.wire.contains(q))
          c,
    ];
  }

  /// Tests [id] and keeps the answer.
  ///
  /// The result is the body, not the status code: the server answers 200 with
  /// `success: false` when the receiver refused, and reporting that as a
  /// success would tell someone their pager works when it does not.
  Future<void> test(String id) async {
    testing = id;
    failure = null;
    notifyListeners();
    try {
      results[id] = await client.testAlertChannel(id);
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = switch (e.status) {
        403 => const SessionFailure('alertsForbidden', ''),
        // 409: the installation has no secrets key, so there is nothing to
        // send with. Saying "forbidden" would send someone to the wrong place.
        409 => const SessionFailure('channelsNoSecrets', ''),
        _ => SessionFailure('unexpected', e.message),
      };
    } finally {
      testing = null;
      notifyListeners();
    }
  }
}

/// The mute windows: what is silenced, and until when.
///
/// The on-call write this screen exists for is "silence everything for the
/// next two hours while we deploy". Recurring schedules are read here and
/// edited on the web; building a weekly-recurrence editor on a phone would
/// produce a worse one than the web already has.
class AlertMutesController extends SectionController<AlertMute> {
  AlertMutesController(super.client);

  /// Which mute is being ended, so only that row is busy.
  String? busy;

  /// True while a new one is being created.
  bool creating = false;

  @override
  String get forbiddenKind => 'alertsForbidden';

  @override
  Future<List<AlertMute>> fetch() async {
    final mutes = [...(await client.alertMutes()).mutes];
    final q = query.trim().toLowerCase();
    final matched = q.isEmpty
        ? mutes
        : [
            for (final m in mutes)
              if (m.name.toLowerCase().contains(q) ||
                  m.comment.toLowerCase().contains(q))
                m,
          ];
    // Active first, then by when they end: what is silencing alerts right now
    // is the thing somebody came here to find.
    matched.sort((a, b) {
      if (a.active != b.active) return a.active ? -1 : 1;
      return a.endsAt.compareTo(b.endsAt);
    });
    return matched;
  }

  /// Silences for [duration] from now.
  Future<void> createFor({
    required String name,
    required Duration duration,
    List<String> ruleIds = const [],
  }) async {
    creating = true;
    failure = null;
    notifyListeners();
    final now = DateTime.now().toUtc();
    try {
      await client.createAlertMute(
        name: name,
        startsAt: now,
        endsAt: now.add(duration),
        ruleIds: ruleIds,
      );
      await refresh();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = e.status == 403
          ? const SessionFailure('alertsForbidden', '')
          : SessionFailure('unexpected', e.message);
    } finally {
      creating = false;
      notifyListeners();
    }
  }

  /// Ends [id] now, by deleting the window.
  Future<void> end(String id) async {
    busy = id;
    failure = null;
    notifyListeners();
    try {
      await client.deleteAlertMute(id);
      await refresh();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      // 404: it expired between the list being drawn and the button being
      // pressed. Reload and let the row go rather than show a red banner.
      if (e.status == 404) {
        await refresh();
      } else {
        failure = e.status == 403
            ? const SessionFailure('alertsForbidden', '')
            : SessionFailure('unexpected', e.message);
      }
    } finally {
      busy = null;
      notifyListeners();
    }
  }
}

/// The routing rules, in the order the server evaluates them.
///
/// The question this screen answers is "why did that page go there": routes
/// are tried in order and the first match wins, so the order is the answer
/// and a list that showed them any other way would be misleading.
class AlertRoutesController extends SectionController<AlertRoutingRule> {
  AlertRoutesController(super.client);

  /// Which rule is being switched, so only that row is busy.
  String? busy;

  /// True while a new order is being saved.
  bool reordering = false;

  @override
  String get forbiddenKind => 'alertsForbidden';

  @override
  Future<List<AlertRoutingRule>> fetch() async {
    final rules = (await client.alertRoutingRules()).routingRules;
    // Evaluation order, which is what `position` means. Not sorted by name,
    // not filtered: a search box that hid a route would hide the reason a
    // page went somewhere.
    return [...rules]..sort((a, b) => a.position.compareTo(b.position));
  }

  /// Moves the rule at [from] to [to] and saves the whole order.
  ///
  /// The list is reordered locally first so the row follows the finger, then
  /// the server is told; a failure reloads, which puts it back.
  Future<void> move(int from, int to) async {
    if (from == to) return;
    final next = [...items];
    final moved = next.removeAt(from);
    next.insert(to > from ? to - 1 : to, moved);
    items = next;
    reordering = true;
    failure = null;
    notifyListeners();
    try {
      // Every id exactly once: the server rejects a partial list rather than
      // reshuffling quietly, so this sends the whole order it just drew.
      await client.reorderAlertRoutingRules([for (final r in next) r.id]);
      await refresh();
    } on ApiUnreachable {
      // Reload first and report after: a successful refresh() clears
      // `failure`, so setting it before the reload throws the message away
      // and leaves an order the server never accepted looking accepted.
      await refresh();
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      final reported = e.status == 403
          ? const SessionFailure('alertsForbidden', '')
          : SessionFailure('unexpected', e.message);
      await refresh();
      failure = reported;
    } finally {
      reordering = false;
      notifyListeners();
    }
  }

  Future<void> setEnabled(
    AlertRoutingRule rule, {
    required bool enabled,
  }) async {
    busy = rule.id;
    failure = null;
    notifyListeners();
    try {
      await client.setAlertRoutingRuleEnabled(rule, enabled: enabled);
      await refresh();
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = e.status == 403
          ? const SessionFailure('alertsForbidden', '')
          : SessionFailure('unexpected', e.message);
    } finally {
      busy = null;
      notifyListeners();
    }
  }
}

/// The holiday calendars: named date lists a recurring mute skips.
///
/// They belong to the mutes screen because that is the only thing that uses
/// them, and the only reason to open this list is a mute that fired on a
/// holiday.
class AlertCalendarsController extends SectionController<AlertHolidayCalendar> {
  AlertCalendarsController(super.client);

  /// Which calendar is being deleted, so only that row is busy.
  String? busy;

  /// True while the form is saving.
  bool saving = false;

  @override
  String get forbiddenKind => 'alertsForbidden';

  @override
  Future<List<AlertHolidayCalendar>> fetch() async =>
      (await client.alertHolidayCalendars()).calendars;

  /// Creates one, or replaces [id] when it is given.
  ///
  /// Returns whether it was saved, so the form can close itself only when the
  /// server took it -- a form that closed on failure would lose the dates
  /// somebody just typed.
  Future<bool> save({
    String? id,
    required String name,
    required String description,
    required List<String> dates,
  }) async {
    saving = true;
    failure = null;
    notifyListeners();
    try {
      if (id == null) {
        await client.createAlertHolidayCalendar(
          name: name,
          description: description,
          dates: dates,
        );
      } else {
        await client.updateAlertHolidayCalendar(
          id,
          name: name,
          description: description,
          dates: dates,
        );
      }
      await refresh();
      return true;
    } on ApiUnreachable {
      await refresh();
      failure = const SessionFailure('unreachable', '');
      return false;
    } on ApiException catch (e) {
      // 409 is a name already taken; the message says which, so it is shown
      // rather than replaced with something about calendars in general.
      final reported = e.status == 403
          ? const SessionFailure('alertsForbidden', '')
          : SessionFailure('unexpected', e.message);
      await refresh();
      failure = reported;
      return false;
    } finally {
      saving = false;
      notifyListeners();
    }
  }

  /// Deletes [id].
  ///
  /// The server answers 409 while a mute still references the calendar, which
  /// is the whole reason the rows show how many use it.
  Future<void> remove(String id) async {
    busy = id;
    failure = null;
    notifyListeners();
    try {
      await client.deleteAlertHolidayCalendar(id);
      await refresh();
    } on ApiUnreachable {
      await refresh();
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      if (e.status == 404) {
        // Already gone. Reload and let the row go rather than say so.
        await refresh();
      } else {
        final reported = e.status == 403
            ? const SessionFailure('alertsForbidden', '')
            : SessionFailure('unexpected', e.message);
        await refresh();
        failure = reported;
      }
    } finally {
      busy = null;
      notifyListeners();
    }
  }
}

/// The delivery log: what was sent, where, and whether it arrived.
///
/// Opened from a channel, or from the channels screen for all of them. The
/// question is always the same one -- "did the page actually go out" -- which
/// is why the filter is the status rather than a search box.
class AlertDeliveriesController extends SectionController<AlertDelivery> {
  AlertDeliveriesController(super.client, {this.channelId = ''});

  /// Empty for every channel. Fixed for the life of the controller: the
  /// screen is opened from one channel or from none.
  final String channelId;

  /// The wire value of [AlertNotificationStatus], or empty for all of them.
  String status = '';

  @override
  String get forbiddenKind => 'alertsForbidden';

  @override
  Future<List<AlertDelivery>> fetch() async =>
      // Filtered by the server, not here: the limit is applied before any
      // filtering, so keeping the failures out of a page of 200 would show
      // the failures among the last 200 notifications rather than the last
      // 200 failures.
      (await client.alertDeliveries(
        channelId: channelId,
        status: status,
      )).deliveries;
}
