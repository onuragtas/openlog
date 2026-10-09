// ignore: unused_import
import 'package:intl/intl.dart' as intl;
import 'app_localizations.dart';

// ignore_for_file: type=lint

/// The translations for English (`en`).
class LEn extends L {
  LEn([String locale = 'en']) : super(locale);

  @override
  String get appTitle => 'openlog';

  @override
  String get serverTitle => 'Connect to openlog';

  @override
  String get serverDescription =>
      'openlog runs on your own server. The address below is the hosted one — replace it with yours if you run your own.';

  @override
  String get serverAddressLabel => 'Server address';

  @override
  String get serverContinue => 'Continue';

  @override
  String get serverChecking => 'Checking…';

  @override
  String get serverInvalidAddress => 'That does not look like an address.';

  @override
  String serverUnreachable(String address) {
    return 'Cannot reach $address. Check the address and your connection.';
  }

  @override
  String get serverNotOpenlog =>
      'That address answered, but not like an openlog server.';

  @override
  String get serverChange => 'Change server';

  @override
  String get signInTitle => 'Sign in to openlog';

  @override
  String get signInDescription =>
      'Use the e-mail address and password of your openlog account.';

  @override
  String get emailLabel => 'E-mail';

  @override
  String get emailHint => 'you@example.com';

  @override
  String get passwordLabel => 'Password';

  @override
  String get signInSubmit => 'Sign in';

  @override
  String get signInChecking => 'Signing in…';

  @override
  String get signInInvalid => 'Wrong e-mail address or password.';

  @override
  String get signInRateLimited =>
      'Too many failed attempts. Wait a few minutes and try again.';

  @override
  String get signInRequired => 'E-mail address and password are required.';

  @override
  String get signInStaticMode =>
      'This server runs with OPENLOG_AUTH_MODE=static and has no user accounts.';

  @override
  String get deviceNameLabel => 'Device name';

  @override
  String get deviceNameHelp =>
      'Shown in your session list, so you can tell this device from your others and sign it out.';

  @override
  String get deviceNameRequired => 'A device name is required.';

  @override
  String get signUpPrompt => 'No account?';

  @override
  String get signUpLink => 'Create an account';

  @override
  String get signUpTitle => 'Create your openlog account';

  @override
  String get signUpDescription =>
      'Start a new organization. You become its owner and can invite your team.';

  @override
  String get nameLabel => 'Your name';

  @override
  String get orgLabel => 'Organization name';

  @override
  String get orgHint => 'e.g. Acme Inc.';

  @override
  String passwordHintMin(int min) {
    return 'At least $min characters, and not your e-mail address.';
  }

  @override
  String get signUpSubmit => 'Create account';

  @override
  String get signUpSubmitting => 'Creating the account…';

  @override
  String get signUpRequired =>
      'Organization name, e-mail address and password are required.';

  @override
  String get signUpDisabled =>
      'Sign-up is closed on this server. Ask an administrator of your organization for an invitation.';

  @override
  String get signUpEmailTaken =>
      'An account with this e-mail address already exists. Sign in instead.';

  @override
  String get signUpRateLimited => 'Too many sign-up attempts. Try again later.';

  @override
  String signUpOnWeb(String address) {
    return 'This server asks for a CAPTCHA when signing up, which this app cannot show. Create the account at $address in a browser, then sign in here.';
  }

  @override
  String get signUpVerificationNote =>
      'We will e-mail you a link to confirm your address.';

  @override
  String get haveAccount => 'Already have an account?';

  @override
  String get toSignIn => 'Sign in';

  @override
  String homeSignedInAs(String email) {
    return 'Signed in as $email';
  }

  @override
  String get homeOrganization => 'Organization';

  @override
  String get homeRole => 'Role';

  @override
  String get homeSignOut => 'Sign out';

  @override
  String get homeNextPhase =>
      'Alerts, services and logs arrive in the next phase.';

  @override
  String get homeSwitchOrganization => 'Switch organization';

  @override
  String get roleOwner => 'Owner';

  @override
  String get roleAdmin => 'Admin';

  @override
  String get roleMember => 'Member';

  @override
  String get roleViewer => 'Viewer';

  @override
  String get roleUnknown => 'Unknown role';

  @override
  String get retry => 'Try again';

  @override
  String get cancel => 'Cancel';

  @override
  String errorUnexpected(String detail) {
    return 'Something went wrong: $detail';
  }

  @override
  String get alertsTitle => 'Alerts';

  @override
  String get alertsEmpty => 'Nothing is firing.';

  @override
  String get alertsEmptyHint =>
      'Open alerts show up here the moment a rule fires.';

  @override
  String get alertsForbidden => 'Your role does not allow reading alerts.';

  @override
  String get alertsAlreadyResolved =>
      'That alert resolved before it could be acknowledged.';

  @override
  String get alertsAcknowledge => 'Acknowledge';

  @override
  String alertsAcknowledgedBy(String email) {
    return 'Acknowledged by $email';
  }

  @override
  String get alertsAcknowledgedUnknown => 'Acknowledged';

  @override
  String alertsCounts(int open, int acknowledged) {
    return '$open open, $acknowledged acknowledged';
  }

  @override
  String alertsResolvedRecently(int count) {
    return '$count resolved in the last 7 days';
  }

  @override
  String get alertsMuted => 'Muted';

  @override
  String get alertsFlapping => 'Flapping';

  @override
  String alertsOpened(String when) {
    return 'Opened $when';
  }

  @override
  String get severityCritical => 'Critical';

  @override
  String get severityWarning => 'Warning';

  @override
  String get severityInfo => 'Info';

  @override
  String get severityUnknown => 'Unknown severity';

  @override
  String get refresh => 'Refresh';

  @override
  String get justNow => 'just now';

  @override
  String minutesAgo(int count) {
    return '${count}m ago';
  }

  @override
  String hoursAgo(int count) {
    return '${count}h ago';
  }

  @override
  String daysAgo(int count) {
    return '${count}d ago';
  }

  @override
  String get navAlerts => 'Alerts';

  @override
  String get navLogs => 'Logs';

  @override
  String get servicesEmpty => 'No service has reported in the last hour.';

  @override
  String get servicesSearch => 'Search services';

  @override
  String get servicesForbidden => 'Your role does not allow reading services.';

  @override
  String get svcThroughput => 'rpm';

  @override
  String get svcErrorRate => 'errors';

  @override
  String get svcP95 => 'p95';

  @override
  String get svcApdex => 'Apdex';

  @override
  String get svcNoData => '—';

  @override
  String get logsEmpty => 'No log records match.';

  @override
  String get logsSearch => 'Search in the message';

  @override
  String get logsForbidden => 'Your role does not allow reading logs.';

  @override
  String get logsSeverity => 'Severity';

  @override
  String get logsSeverityAll => 'All';

  @override
  String get logsNoService => 'no service';

  @override
  String get navDashboards => 'Dashboards';

  @override
  String get dashboardsEmpty => 'No dashboard yet.';

  @override
  String get dashboardsSearch => 'Search dashboards';

  @override
  String get dashboardsForbidden =>
      'Your role does not allow reading dashboards.';

  @override
  String dashboardWidgets(int count, int pages) {
    return '$count widgets on $pages pages';
  }

  @override
  String get dashboardNoQuery => 'Nothing to run';

  @override
  String get dashboardWidgetFailed => 'This widget\'s query did not answer.';

  @override
  String get dashboardOnWeb => 'Best read on the web';

  @override
  String get dashboardNoData => 'No data';

  @override
  String get dashboardLoading => 'Running the queries…';

  @override
  String get navApm => 'APM';

  @override
  String get navSettings => 'Settings';

  @override
  String get navMain => 'Main navigation';

  @override
  String get settingsAccount => 'Account';

  @override
  String get closeMenu => 'Close menu';

  @override
  String get navHosts => 'Hosts';

  @override
  String get navContainers => 'Containers';

  @override
  String get navKubernetes => 'Kubernetes';

  @override
  String get navDatabases => 'Databases';

  @override
  String get navSlos => 'SLOs';

  @override
  String get navSynthetics => 'Synthetics';

  @override
  String get navJobs => 'Job monitoring';

  @override
  String get navVulnerabilities => 'Vulnerabilities';

  @override
  String get sectionForbidden =>
      'Your role does not allow reading this section.';

  @override
  String get sectionSearch => 'Search';

  @override
  String get hostsEmpty => 'No host is reporting.';

  @override
  String get containersEmpty => 'No container is reporting.';

  @override
  String get podsEmpty => 'No pod found.';

  @override
  String get databasesEmpty => 'No database instance is reporting.';

  @override
  String get slosEmpty => 'No SLO defined.';

  @override
  String get syntheticsEmpty => 'No synthetic check defined.';

  @override
  String get jobsEmpty => 'No job monitor defined.';

  @override
  String get vulnerabilitiesEmpty => 'No vulnerability found.';

  @override
  String get statCpu => 'CPU';

  @override
  String get statMemory => 'Memory';

  @override
  String get statDisk => 'Disk';

  @override
  String get statRestarts => 'restarts';

  @override
  String get statUptime => 'uptime';

  @override
  String get statRuns => 'runs';

  @override
  String get statFailures => 'failures';

  @override
  String get statBudget => 'budget left';

  @override
  String get statObjective => 'objective';

  @override
  String get statScore => 'score';

  @override
  String get statHosts => 'hosts';

  @override
  String get statCalls => 'calls';

  @override
  String get stateDisabled => 'Disabled';

  @override
  String get stateNotReporting => 'Not reporting';

  @override
  String get stateReady => 'Ready';

  @override
  String get sloMet => 'Met';

  @override
  String get sloBreached => 'Breached';

  @override
  String get detailGone => 'That is no longer on the server.';

  @override
  String get incidentTimeline => 'Timeline';

  @override
  String get incidentNoEvents => 'Nothing has happened to this incident yet.';

  @override
  String get incidentDeliveries => 'Notifications';

  @override
  String get incidentNoDeliveries => 'Nothing was sent for this incident.';

  @override
  String get incidentLabels => 'Labels';

  @override
  String get incidentValue => 'Value';

  @override
  String get incidentThreshold => 'Threshold';

  @override
  String incidentOpenService(String name) {
    return 'Open $name';
  }

  @override
  String get incidentResolve => 'Resolve';

  @override
  String get incidentResolveHint =>
      'A series that is still breaching opens a new incident, so this does not silence anything.';

  @override
  String incidentResolvedBy(String email) {
    return 'Resolved by $email';
  }

  @override
  String incidentResolved(String when) {
    return 'Resolved $when';
  }

  @override
  String get incidentNote => 'Add a note';

  @override
  String get incidentNoteHint => 'What you found';

  @override
  String get incidentNoteSend => 'Send';

  @override
  String get incidentEventOpened => 'Opened';

  @override
  String get incidentEventFlapping => 'Flapping';

  @override
  String get incidentEventAcknowledged => 'Acknowledged';

  @override
  String get incidentEventNote => 'Note';

  @override
  String get incidentEventRenotified => 'Re-notified';

  @override
  String get incidentEventResolved => 'Resolved';

  @override
  String get incidentEventDelivered => 'Notification delivered';

  @override
  String get incidentEventFailed => 'Notification failed';

  @override
  String get incidentEventSuppressed => 'Notification suppressed';

  @override
  String get incidentEventMuted => 'Notification muted';

  @override
  String get incidentEventUnknown => 'Event this app does not know';

  @override
  String get serviceSignals => 'Golden signals';

  @override
  String get serviceRequests => 'Requests';

  @override
  String get serviceErrors => 'Errors';

  @override
  String get serviceLatency => 'Latency';

  @override
  String get serviceNoData => 'This service has not reported in the window.';

  @override
  String get serviceThroughputChart => 'Requests per minute';

  @override
  String get serviceErrorRateChart => 'Error rate';

  @override
  String get deliveryPending => 'Pending';

  @override
  String get deliverySending => 'Sending';

  @override
  String get deliveryDelivered => 'Delivered';

  @override
  String get deliveryFailed => 'Failed';

  @override
  String get deliverySuppressed => 'Suppressed';

  @override
  String get deliveryUnknown => 'Unknown state';

  @override
  String deliveryAttempts(int count) {
    return '$count attempts';
  }

  @override
  String get serviceTabOverview => 'Overview';

  @override
  String get serviceTabErrors => 'Errors';

  @override
  String get errorsEmpty => 'Nothing has thrown in the window.';

  @override
  String errorsOccurrences(int count) {
    return '$count times';
  }

  @override
  String errorsLastSeen(String when) {
    return 'last $when';
  }

  @override
  String get errorStatusUnresolved => 'Unresolved';

  @override
  String get errorStatusResolved => 'Resolved';

  @override
  String get errorStatusIgnored => 'Ignored';

  @override
  String get errorStatusUnknown => 'Unknown state';

  @override
  String get errorsNoTrace => 'No trace was kept for this error.';

  @override
  String get errorsTruncated => 'There are more; this is the top of the list.';

  @override
  String get traceTitle => 'Trace';

  @override
  String traceSpans(int count) {
    return '$count spans';
  }

  @override
  String get traceEmpty => 'This trace has no spans.';

  @override
  String get traceRoot => 'root';

  @override
  String get navQuery => 'Query';

  @override
  String get queryHint => 'SELECT count(*) FROM logs SINCE 1 hour ago';

  @override
  String get queryRun => 'Run';

  @override
  String get queryEmpty => 'Write a query and press Run.';

  @override
  String get queryForbidden => 'Your role does not allow running queries.';

  @override
  String queryRejected(String detail) {
    return 'The server would not run that: $detail';
  }

  @override
  String queryMeta(int rows, int ms) {
    return '$rows rows read in $ms ms';
  }

  @override
  String get queryRecent => 'Recent';

  @override
  String get queryTruncated =>
      'The answer was cut short by the server\'s limit.';

  @override
  String get navTraces => 'Traces';

  @override
  String get tracesEmpty => 'No request has been traced in the window.';

  @override
  String get tracesNewest => 'Newest';

  @override
  String get tracesSlowest => 'Slowest';

  @override
  String get tracesError => 'error';

  @override
  String get navMetrics => 'Metrics';

  @override
  String get metricsEmpty => 'No metric has reported in the window.';

  @override
  String metricSeriesCount(int count) {
    return '$count series';
  }

  @override
  String metricNoChart(String detail) {
    return 'No series could be drawn: $detail';
  }

  @override
  String get metricNoPoints => 'This metric has no points in the window.';

  @override
  String get metricAttributes => 'Attributes';

  @override
  String get metricResourceKeys => 'Resource keys';

  @override
  String get metricTruncated => 'More series than the chart shows.';

  @override
  String metricOneSeries(int count) {
    return 'One of $count series.';
  }

  @override
  String get navRum => 'Browser';

  @override
  String get rumEmpty => 'No browser application has reported in the window.';

  @override
  String get rumViews => 'Page views';

  @override
  String get rumSessions => 'Sessions';

  @override
  String get rumErrors => 'Errors';

  @override
  String get rumAvgLoad => 'Average load';

  @override
  String get rumVitals => 'Core Web Vitals';

  @override
  String get rumVitalGood => 'good';

  @override
  String get rumVitalNeedsImprovement => 'needs work';

  @override
  String get rumVitalPoor => 'poor';

  @override
  String get rumVitalNoData => 'no measurements';

  @override
  String rumVitalShare(int percent) {
    return '$percent% good';
  }

  @override
  String get rumNoPoints => 'No page view in the window.';

  @override
  String get navCosts => 'Costs';

  @override
  String get costsOff => 'This installation does not estimate cost.';

  @override
  String get costsTotal => 'Total';

  @override
  String get costsPerHour => 'Per hour';

  @override
  String get costsIdle => 'Idle';

  @override
  String get costsHosts => 'Hosts';

  @override
  String costsEstimate(String updated, String note) {
    return 'An estimate from a price table, not a bill. Prices collected $updated. $note';
  }

  @override
  String costsUnpriced(int count) {
    return '$count hosts have no price.';
  }

  @override
  String get costsHostsTitle => 'Most expensive hosts';

  @override
  String costsUsed(int percent) {
    return '$percent% used';
  }

  @override
  String get costsNoPrice => 'no price';

  @override
  String costsMoreHosts(int count) {
    return 'and $count more';
  }

  @override
  String get navInventory => 'Inventory search';

  @override
  String get inventoryEmpty => 'Nothing matches in this category.';

  @override
  String get inventoryHint =>
      'Find items across all hosts — e.g. which hosts have openssl?';

  @override
  String get inventoryCategory => 'Category';

  @override
  String get inventorySearch => 'Key contains';

  @override
  String get invOs => 'OS';

  @override
  String get invHardware => 'Hardware';

  @override
  String get invPackage => 'Packages';

  @override
  String get invProcess => 'Processes';

  @override
  String get invListeningPort => 'Listening ports';

  @override
  String get invSystemdUnit => 'systemd units';

  @override
  String get invKernelModule => 'Kernel modules';

  @override
  String get invNetworkInterface => 'Network interfaces';

  @override
  String get invMount => 'Mounts';

  @override
  String get invUser => 'Users';

  @override
  String get invLaunchdService => 'launchd services';

  @override
  String get invWindowsService => 'Windows services';

  @override
  String get invDiscoveredService => 'Discovered services';

  @override
  String get navFleet => 'Fleet';

  @override
  String get fleetEmpty => 'No agent has reported.';

  @override
  String get fleetAgents => 'Agents';

  @override
  String get fleetOutdated => 'Outdated';

  @override
  String get fleetInProgress => 'Updating';

  @override
  String get fleetFailed => 'Failed';

  @override
  String fleetLatest(String version) {
    return 'Latest $version';
  }

  @override
  String get fleetNoCatalog =>
      'No release catalogue; the server cannot tell what is latest.';

  @override
  String get fleetReadOnly =>
      'Read-only here. Rollouts and policy are changed on the web.';

  @override
  String get fleetUnsupported => 'unsupported';

  @override
  String fleetStatePrefix(String state) {
    return 'update $state';
  }

  @override
  String get fleetStateIdle => 'idle';

  @override
  String get fleetStateDownloading => 'downloading';

  @override
  String get fleetStateVerifying => 'verifying';

  @override
  String get fleetStateStaged => 'staged';

  @override
  String get fleetStateRestarting => 'restarting';

  @override
  String get fleetStateConfirming => 'confirming';

  @override
  String get fleetStateSucceeded => 'succeeded';

  @override
  String get fleetStateFailed => 'failed';

  @override
  String get fleetStateRolledBack => 'rolled back';

  @override
  String get navIntegrations => 'Integrations';

  @override
  String get integrationsEmpty =>
      'The agents have not discovered an integration.';

  @override
  String get integrationsSearch => 'Search integrations';

  @override
  String get integEnabled => 'collecting';

  @override
  String get integNeedsConfig => 'needs configuration';

  @override
  String get integError => 'error';

  @override
  String get integNotAvailable => 'not available';

  @override
  String integCounts(int enabled, int needs, int error) {
    return '$enabled collecting, $needs need configuration, $error failing';
  }

  @override
  String get integConfigureOnWeb =>
      'Configure on the web; this screen reads what the agents report.';

  @override
  String get navProfiles => 'Profiling';

  @override
  String get profilesEmpty => 'Nothing has been profiled in the window.';

  @override
  String get profilesSearch => 'Search services and types';

  @override
  String profileSamples(int count) {
    return '$count samples';
  }

  @override
  String get profileFunctions => 'By self time';

  @override
  String get profileNoFunctions => 'No function carries any of this profile.';

  @override
  String profileShare(String percent) {
    return '$percent%';
  }

  @override
  String get profileOfShown =>
      'Shares are of the rows shown, not of the whole window.';

  @override
  String get navAddData => 'Add data';

  @override
  String get addDataOtlpHttp => 'OTLP over HTTP';

  @override
  String get addDataOtlpGrpc => 'OTLP over gRPC';

  @override
  String get addDataCopied => 'Copied';

  @override
  String get addDataVersions => 'Versions';

  @override
  String addDataServer(String version) {
    return 'Server $version';
  }

  @override
  String addDataAgent(String version, String channel) {
    return 'Agents pinned to $version ($channel)';
  }

  @override
  String get addDataAgentDev =>
      'A development build; the commands pin nothing.';

  @override
  String get addDataBrowser => 'Browser data';

  @override
  String addDataCorsOn(String origins) {
    return 'Allowed from: $origins';
  }

  @override
  String get addDataCorsOff =>
      'Not configured, so a browser cannot send to this server.';

  @override
  String get addDataSourcesOnWeb =>
      'The data sources and their install commands are on the web; this screen has what you need to point something at this server.';

  @override
  String get addDataEndpointDerived =>
      'Derived, not configured — check it is reachable from outside.';

  @override
  String get onboardingForbidden => 'Your role does not allow reading this.';

  @override
  String get hostRuns => 'Running here';

  @override
  String get hostNoServices =>
      'The agent has not reported a snapshot for this host yet.';

  @override
  String hostServicesFailed(String detail) {
    return 'What runs here could not be read: $detail';
  }

  @override
  String get hostNothingFound => 'The agent found nothing it recognises.';

  @override
  String get hostCpu => 'CPU';

  @override
  String get hostMemory => 'Memory';

  @override
  String get hostDisk => 'Disk';

  @override
  String get hostLoad => 'Load';

  @override
  String hostAgent(String version) {
    return 'Agent $version';
  }

  @override
  String get hostAttributes => 'Resource attributes';
}
