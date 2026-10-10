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
import 'audit.dart';
import 'cloud.dart';
import 'api/schema.g.dart';
import 'dashboards.dart';
import 'databases.dart';
import 'detail.dart';
import 'agents.dart';
import 'discovery.dart';
import 'fields.dart';
import 'errors.dart';
import 'integration_settings.dart';
import 'keys.dart';
import 'kubernetes.dart';
import 'list_controller.dart';
import 'logs.dart';
import 'members.dart';
import 'session.dart';
import 'volume.dart';
import 'sso.dart';
import 'usage.dart';
import 'oql.dart';
import 'query.dart';
import 'sampling.dart';
import 'saved_views.dart';
import 'templates.dart';
import 'time_range.dart';
import 'services.dart';

/// Every section's controller, for one signed-in client.
///
/// One object rather than thirteen parameters: the shell needs all of them,
/// the app has to dispose all of them, and a list that long threaded through
/// two widgets is a list that will one day be missing an entry.
class Sections {
  Sections({
    required OpenlogClient client,
    TimeRangeController? range,
    HostsController? hosts,
    ContainersController? containers,
    PodsController? pods,
    PodsController Function()? scopedPods,
    KubernetesScope? k8s,
    KubernetesClusterController? k8sCluster,
    KubernetesNodesController? k8sNodes,
    KubernetesWorkloadsController? k8sWorkloads,
    KubernetesEventsController? k8sEvents,
    ServicesController? services,
    DatabasesController? databases,
    DbActivityController Function(String instance)? dbActivity,
    DbQueriesController Function(String instance)? dbQueries,
    DbSessionsController Function(String instance)? dbSessions,
    DbQueryController Function({
      required String instance,
      required String fingerprint,
    })?
    dbQuery,
    SlosController? slos,
    SyntheticsController? synthetics,
    JobsController? jobs,
    VulnerabilitiesController? vulnerabilities,
    LogsController? logs,
    LogPatternsController? logPatterns,
    VolumeController? logVolume,
    VolumeController? traceVolume,
    SavedViewsController? logViews,
    SavedViewsController? traceViews,
    FieldsController Function(String signal)? fields,
    TracesController? traces,
    MetricsController? metrics,
    RumController? rum,
    CostsController? costs,
    InventoryController? inventory,
    FleetController? fleet,
    IntegrationsController? integrations,
    IntegrationSettingsController Function(String hostId)? integrationSettings,
    CloudConnectionsController? cloud,
    CloudRunsController Function(String id)? cloudRuns,
    ProfilesController? profiles,
    OnboardingController? onboarding,
    SessionsController? sessions,
    AccountController? account,
    OrgController? org,
    PrivacyController? privacy,
    MembersController? members,
    LicenseKeysController? licenseKeys,
    ApiKeysController? apiKeys,
    BrowserKeysController? browserKeys,
    SourceMapsController? sourceMaps,
    AuditController? audit,
    UsageController? usage,
    StorageController? storage,
    SsoController? sso,
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
    OqlSchemaController? oqlSchema,
    OqlValidationController? oqlValidation,
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
    ProfileFlameController Function({
      required String service,
      required String type,
      required String environment,
    })?
    profileFlame,
  }) : range = range ?? TimeRangeController(),
       incident = incident ?? ((id) => IncidentController(client, id)),
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
       profileFlame =
           profileFlame ??
           (({
             required String service,
             required String type,
             required String environment,
           }) => ProfileFlameController(
             client,
             service: service,
             type: type,
             environment: environment,
           )),
       hosts = hosts ?? HostsController(client),
       containers = containers ?? ContainersController(client),
       pods = pods ?? PodsController(client),
       scopedPods = scopedPods ?? (() => PodsController(client)),
       k8s = k8s ?? KubernetesScope(client),
       k8sCluster = k8sCluster ?? KubernetesClusterController(client),
       k8sNodes = k8sNodes ?? KubernetesNodesController(client),
       k8sWorkloads = k8sWorkloads ?? KubernetesWorkloadsController(client),
       k8sEvents = k8sEvents ?? KubernetesEventsController(client),
       services = services ?? ServicesController(client),
       databases = databases ?? DatabasesController(client),
       dbActivity =
           dbActivity ?? ((instance) => DbActivityController(client, instance)),
       dbQueries =
           dbQueries ?? ((instance) => DbQueriesController(client, instance)),
       dbSessions =
           dbSessions ?? ((instance) => DbSessionsController(client, instance)),
       dbQuery =
           dbQuery ??
           (({required String instance, required String fingerprint}) =>
               DbQueryController(
                 client,
                 instance: instance,
                 fingerprint: fingerprint,
               )),
       slos = slos ?? SlosController(client),
       synthetics = synthetics ?? SyntheticsController(client),
       jobs = jobs ?? JobsController(client),
       vulnerabilities = vulnerabilities ?? VulnerabilitiesController(client),
       logs = logs ?? LogsController(client),
       logPatterns = logPatterns ?? LogPatternsController(client),
       logVolume = logVolume ?? VolumeController(client, signal: 'logs'),
       traceVolume = traceVolume ?? VolumeController(client, signal: 'traces'),
       logViews = logViews ?? SavedViewsController(client, signal: 'logs'),
       traceViews =
           traceViews ?? SavedViewsController(client, signal: 'traces'),
       fields =
           fields ?? ((signal) => FieldsController(client, signal: signal)),
       traces = traces ?? TracesController(client),
       metrics = metrics ?? MetricsController(client),
       rum = rum ?? RumController(client),
       costs = costs ?? CostsController(client),
       inventory = inventory ?? InventoryController(client),
       fleet = fleet ?? FleetController(client),
       integrations = integrations ?? IntegrationsController(client),
       integrationSettings =
           integrationSettings ??
           ((hostId) => IntegrationSettingsController(client, hostId: hostId)),
       cloud = cloud ?? CloudConnectionsController(client),
       cloudRuns = cloudRuns ?? ((id) => CloudRunsController(client, id)),
       profiles = profiles ?? ProfilesController(client),
       onboarding = onboarding ?? OnboardingController(client),
       sessions = sessions ?? SessionsController(client),
       account = account ?? AccountController(client),
       org = org ?? OrgController(client),
       privacy = privacy ?? PrivacyController(client),
       members = members ?? MembersController(client),
       licenseKeys = licenseKeys ?? LicenseKeysController(client),
       apiKeys = apiKeys ?? ApiKeysController(client),
       browserKeys = browserKeys ?? BrowserKeysController(client),
       sourceMaps = sourceMaps ?? SourceMapsController(client),
       audit = audit ?? AuditController(client),
       usage = usage ?? UsageController(client),
       storage = storage ?? StorageController(client),
       sso = sso ?? SsoController(client),
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
       oqlSchema = oqlSchema ?? OqlSchemaController(client),
       oqlValidation = oqlValidation ?? OqlValidationController(client),
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
       alerts = alerts ?? AlertsController(client) {
    // Every ranged request carries the chosen window, resolved when it is
    // made. This is the one place that has both the client and the range,
    // and threading a window argument through forty call sites would be
    // forty chances to forget one.
    client.window = () => this.range.value;
  }

  /// The window every screen is about, as the web's URL range is.
  final TimeRangeController range;

  final HostsController hosts;
  final ContainersController containers;
  final PodsController pods;

  /// One node's or one workload's pods, on a screen of its own: a list made
  /// for that screen rather than the tab's, which is still about the
  /// cluster the person left behind.
  final PodsController Function() scopedPods;

  /// The Kubernetes screen: the chosen cluster, and the three lists that
  /// follow it.
  final KubernetesScope k8s;
  final KubernetesClusterController k8sCluster;
  final KubernetesNodesController k8sNodes;
  final KubernetesWorkloadsController k8sWorkloads;
  final KubernetesEventsController k8sEvents;
  final ServicesController services;
  final DatabasesController databases;

  /// One instance's three tabs and one statement, made per screen: they
  /// are about an instance the person opened, not about the section.
  final DbActivityController Function(String instance) dbActivity;
  final DbQueriesController Function(String instance) dbQueries;
  final DbSessionsController Function(String instance) dbSessions;
  final DbQueryController Function({
    required String instance,
    required String fingerprint,
  })
  dbQuery;
  final SlosController slos;
  final SyntheticsController synthetics;
  final JobsController jobs;
  final VulnerabilitiesController vulnerabilities;
  final LogsController logs;

  /// The same logs, grouped by what they say.
  final LogPatternsController logPatterns;

  /// How much arrived and when, above each explorer.
  final VolumeController logVolume;
  final VolumeController traceVolume;

  /// The explorer views somebody kept, per signal. Shared with the web:
  /// one saved here opens in a browser and the other way round.
  final SavedViewsController logViews;
  final SavedViewsController traceViews;

  /// The dictionary a filter is built from, one per signal. Made per
  /// sheet, because it holds which key is being looked at.
  final FieldsController Function(String signal) fields;
  final TracesController traces;
  final MetricsController metrics;
  final RumController rum;
  final CostsController costs;
  final InventoryController inventory;
  final FleetController fleet;
  final IntegrationsController integrations;

  /// What one host's integrations were told: made per host, because a
  /// setting is about the host whose instance is being configured.
  final IntegrationSettingsController Function(String hostId)
  integrationSettings;

  /// The cloud accounts openlog polls, reached from the integrations
  /// section as they are on the web, and one connection's polls.
  final CloudConnectionsController cloud;
  final CloudRunsController Function(String id) cloudRuns;
  final ProfilesController profiles;
  final OnboardingController onboarding;
  final SessionsController sessions;

  /// The account behind the token: its password and its language.
  final AccountController account;

  /// The organization itself: its name, its ids, its language.
  final OrgController org;

  /// What this account may ask for: an export, a deletion.
  final PrivacyController privacy;

  /// Who is in the organization, and who has been asked to join.
  final MembersController members;

  /// The three kinds of key the settings tabs manage.
  final LicenseKeysController licenseKeys;
  final ApiKeysController apiKeys;
  final BrowserKeysController browserKeys;

  /// The source maps that un-minify browser stacks, and the organization's
  /// own record of what changed.
  final SourceMapsController sourceMaps;
  final AuditController audit;

  /// What the organization used this period, against its plan.
  final UsageController usage;

  /// How full the ClickHouse disks are.
  final StorageController storage;

  /// Single sign-on: connections, domains, enforcement, mappings, SCIM.
  final SsoController sso;

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

  /// What OQL offers and what is wrong with what was typed. Both belong to
  /// the console, and both are asked for only once it is opened.
  final OqlSchemaController oqlSchema;
  final OqlValidationController oqlValidation;
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

  /// The same profile as a tree of frames, which is the other tab.
  final ProfileFlameController Function({
    required String service,
    required String type,
    required String environment,
  })
  profileFlame;

  /// Everything that has to be disposed, in the order the drawer lists them,
  /// which is the web's order. Calendars are in here too although the drawer
  /// does not list them: they are reached from the mutes screen, and a
  /// controller left out of this list is a controller never disposed.
  List<ChangeNotifier> get all => [
    onboarding,
    sessions,
    account,
    org,
    privacy,
    members,
    licenseKeys,
    apiKeys,
    browserKeys,
    sourceMaps,
    audit,
    usage,
    storage,
    sso,
    hosts,
    containers,
    costs,
    pods,
    k8s,
    k8sCluster,
    k8sNodes,
    k8sWorkloads,
    k8sEvents,
    integrations,
    cloud,
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
    logPatterns,
    logVolume,
    traceVolume,
    logViews,
    traceViews,
    traces,
    metrics,
    query,
    oqlSchema,
    oqlValidation,
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

  /// A different window is a different question, so what was loaded for
  /// the old one is not an answer any more.
  ///
  /// Marked stale rather than reloaded: reloading thirteen sections at
  /// once would be thirteen requests for screens nobody is looking at.
  /// Each one asks again the first time it is looked at, which is the
  /// same rule as the first load.
  void markRangeStale() {
    for (final c in all) {
      if (c is ListController) c.loaded = false;
      if (c is DetailController) c.loaded = false;
    }
  }

  void dispose() {
    for (final c in all) {
      c.dispose();
    }
    range.dispose();
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

  /// The web's filters. [composeProject] is null for every project and ""
  /// for the containers that belong to none, which is a filter the web
  /// offers as its own option.
  String hostId = '';
  String? composeProject;
  String state = '';

  /// Grouped by compose service, as the web's toggle does. The grouping
  /// itself happens where the rows are drawn; this is only the switch.
  bool grouped = false;

  int total = 0;

  /// The projects to choose from, from `containers/groups`.
  List<ComposeProject> projects = const [];

  /// Which host the projects were asked for, so changing the host asks
  /// again rather than offering projects of a host nobody is looking at.
  String _projectsOf = '-';

  @override
  Future<List<ApiContainer>> fetch() async {
    if (_projectsOf != hostId) {
      projects = (await client.containerGroups(hostId: hostId)).projects;
      _projectsOf = hostId;
    }
    final page = await client.containers(
      q: query,
      hostId: hostId,
      composeProject: composeProject,
      state: state,
    );
    total = page.total;
    return page.containers;
  }
}

/// The container states the server filters by, in the contract's order.
const containerStates = [
  'running',
  'paused',
  'restarting',
  'exited',
  'created',
  'dead',
  'removing',
  'unknown',
];

/// One compose service's containers, as the web groups them.
class ContainerGroup {
  ContainerGroup(this.project, this.service);

  final String project;
  final String service;
  final containers = <ApiContainer>[];
  int running = 0;
  double? cpu;
  double? memory;

  /// True for the containers that belong to no compose project, which the
  /// web puts last and labels "tek başına".
  bool get standalone => project.isEmpty && service.isEmpty;
}

/// Groups containers by compose project and service, the web's own way:
/// only reporting containers count towards the numbers, and the ones in no
/// project come last.
List<ContainerGroup> groupByComposeService(List<ApiContainer> containers) {
  final index = <String, ContainerGroup>{};
  final order = <ContainerGroup>[];
  for (final c in containers) {
    final key = c.composeProject.isNotEmpty || c.composeService.isNotEmpty
        ? '${c.composeProject}/${c.composeService}'
        : '';
    final g = index.putIfAbsent(key, () {
      final made = ContainerGroup(c.composeProject, c.composeService);
      order.add(made);
      return made;
    });
    g.containers.add(c);
    if (!c.reporting) continue;
    if (c.state == 'running') g.running++;
    if (c.cpuUtilization != null) g.cpu = (g.cpu ?? 0) + c.cpuUtilization!;
    if (c.memoryUsage != null) g.memory = (g.memory ?? 0) + c.memoryUsage!;
  }
  return [
    for (final g in order)
      if (!g.standalone) g,
    for (final g in order)
      if (g.standalone) g,
  ];
}

class PodsController extends SectionController<KubernetesPod> {
  PodsController(super.client);

  /// The web's own filters. Empty means "every one of them", which is what
  /// the server does with a missing parameter.
  String clusterUid = '';
  String namespace = '';
  String phase = '';

  /// Set when this list was opened from somewhere -- a node's pods, a
  /// workload's pods -- rather than chosen here.
  String node = '';
  String workloadKind = '';
  String workloadName = '';

  /// How many matched before the server's limit.
  int total = 0;

  @override
  Future<List<KubernetesPod>> fetch() async {
    final page = await client.pods(
      q: query,
      clusterUid: clusterUid,
      namespace: namespace,
      phase: phase,
      node: node,
      workloadKind: workloadKind,
      workloadName: workloadName,
    );
    total = page.total;
    return page.pods;
  }
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

  /// What the filter builder added, AND-ed with the search box.
  List<Filter> filters = const [];

  /// Slowest instead of newest. The two questions a traces list answers are
  /// "what just happened" and "what is slow", and on a phone a switch between
  /// them beats the web's sort menu.
  bool slowest = false;

  @override
  Future<List<SpanQueryRow>> fetch() async => (await client.traces(
    q: query.trim(),
    slowest: slowest,
    filters: [for (final f in filters) f.toJson()],
  )).rows;
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

  /// How the fleet updates itself, and the rollouts it has run. One fetch
  /// for the three, because the screen shows them together and three
  /// windows of the same fleet would contradict each other.
  FleetPolicy? policy;
  List<FleetRollout> rollouts = const [];

  /// True while a rollout action is in flight, so the buttons can say so
  /// rather than let two pauses race.
  bool acting = false;

  @override
  Future<List<FleetHost>> fetch() async {
    final answers = await Future.wait([
      client.fleetSummary(),
      client.fleetHosts(q: query.trim()),
      client.fleetPolicy(),
      client.fleetRollouts(),
    ]);
    summary = answers[0] as FleetSummary;
    policy = answers[2] as FleetPolicy;
    rollouts = (answers[3] as FleetRolloutPage).rollouts;
    return (answers[1] as FleetHostPage).hosts;
  }

  /// Runs one rollout action and reloads: pausing a rollout changes the
  /// summary, the host list and the rollout itself.
  Future<bool> act(Future<void> Function() action) async {
    if (acting) return false;
    acting = true;
    failure = null;
    notifyListeners();
    try {
      await action();
      return true;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
      return false;
    } on ApiException catch (e) {
      failure = e.status == 403
          ? const SessionFailure('fleetForbidden', '')
          : SessionFailure('unexpected', e.message);
      return false;
    } finally {
      acting = false;
      notifyListeners();
      if (failure == null) await refresh();
    }
  }

  /// Changes one field of the policy and sends the rest back as it came:
  /// the contract takes the whole policy, and a PUT that left the waves
  /// out would quietly reset them.
  Future<bool> setMode(String mode) {
    final p = policy;
    if (p == null) return Future.value(false);
    return act(
      () => client.putFleetPolicy({
        'mode': mode,
        'channel': p.channel.wire,
        'target': p.target.wire,
        'pinned_version': p.pinnedVersion,
        'waves': p.waves,
        'wave_soak_minutes': p.waveSoakMinutes,
        'halt_failure_rate': p.haltFailureRate,
        'maintenance_windows': [
          for (final w in p.maintenanceWindows) w.toJson(),
        ],
      }),
    );
  }
}

/// The versions the fleet may be rolled back to: the versions it is
/// running that are older than the one it is rolling towards, newest
/// first. The web's own rule.
List<String> rollbackCandidates(List<String> running, String? from) {
  final seen = <String>{};
  final out = [
    for (final v in running)
      if (_isVersion(v) &&
          (from == null || _compareVersions(v, from) < 0) &&
          seen.add(v))
        v,
  ];
  out.sort((a, b) => _compareVersions(b, a));
  return out;
}

bool _isVersion(String v) =>
    RegExp(r'^\d+\.\d+\.\d+').hasMatch(v.startsWith('v') ? v.substring(1) : v);

int _compareVersions(String a, String b) {
  List<int> parts(String v) => [
    for (final p in (v.startsWith('v') ? v.substring(1) : v).split('.'))
      int.tryParse(RegExp(r'^\d+').stringMatch(p) ?? '') ?? 0,
  ];
  final x = parts(a);
  final y = parts(b);
  for (var i = 0; i < 3; i++) {
    final d = (i < x.length ? x[i] : 0) - (i < y.length ? y[i] : 0);
    if (d != 0) return d;
  }
  return 0;
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
