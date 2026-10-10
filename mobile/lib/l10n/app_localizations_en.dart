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
  String get errorsEmpty => 'No errors with this filter.';

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
  String get errorsTruncated => 'Not every matching group; narrow the filter.';

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

  @override
  String get containerCpu => 'CPU';

  @override
  String get containerMemory => 'Memory';

  @override
  String get containerNetwork => 'Network';

  @override
  String get containerDisk => 'Block I/O';

  @override
  String get containerNoSeries => 'Nothing was sampled in the window.';

  @override
  String containerSeriesFailed(String detail) {
    return 'The charts could not be read: $detail';
  }

  @override
  String get containerNoLimit =>
      'No memory limit, so there is no share to show.';

  @override
  String containerRestarts(int count) {
    return '$count restarts';
  }

  @override
  String get containerImage => 'Image';

  @override
  String get containerAttributes => 'Attributes';

  @override
  String containerOn(String host) {
    return 'on $host';
  }

  @override
  String get containerRxTx => 'in / out';

  @override
  String get containerReadWrite => 'read / write';

  @override
  String get podContainers => 'Containers';

  @override
  String get podEvents => 'Events';

  @override
  String get podNoEvents => 'No event about this pod.';

  @override
  String podEventsFailed(String detail) {
    return 'The events could not be read: $detail';
  }

  @override
  String get podLabels => 'Labels';

  @override
  String get podServices => 'Services';

  @override
  String get podRestartsLabel => 'Restarts';

  @override
  String get podNode => 'Node';

  @override
  String get podReady => 'ready';

  @override
  String get podNotReady => 'not ready';

  @override
  String podEventCount(int count) {
    return '$count×';
  }

  @override
  String get podCpu => 'CPU';

  @override
  String get podMemory => 'Working set';

  @override
  String get navRules => 'Rules';

  @override
  String get rulesEmpty => 'No alert rule.';

  @override
  String get rulesSearch => 'Search rules';

  @override
  String get ruleFiring => 'firing';

  @override
  String get rulePending => 'pending';

  @override
  String get ruleOk => 'ok';

  @override
  String get ruleError => 'error';

  @override
  String get ruleDisabled => 'off';

  @override
  String ruleOpenIncidents(int count) {
    return '$count open';
  }

  @override
  String ruleEvery(int seconds) {
    return 'every ${seconds}s';
  }

  @override
  String get ruleDisableTitle => 'Turn this rule off?';

  @override
  String ruleDisableBody(int count) {
    return 'It will stop evaluating, and its $count open incidents are resolved as well. Anything it is paging about now stops being tracked.';
  }

  @override
  String get ruleDisableBodyNone =>
      'It will stop evaluating until someone turns it back on.';

  @override
  String get ruleDisableConfirm => 'Turn off';

  @override
  String get ruleEnableTitle => 'Turn this rule back on?';

  @override
  String get ruleEnableConfirm => 'Turn on';

  @override
  String get ruleCancel => 'Cancel';

  @override
  String get ruleEditOnWeb =>
      'Thresholds, conditions and channels are edited on the web.';

  @override
  String get navChannels => 'Channels';

  @override
  String get channelsEmpty => 'No notification channel.';

  @override
  String get channelsSearch => 'Search channels';

  @override
  String get channelsNoSecrets =>
      'This installation has no secrets key, so channels cannot be stored or tested.';

  @override
  String get channelTest => 'Send a test';

  @override
  String channelTestOk(int ms) {
    return 'Reached in $ms ms';
  }

  @override
  String channelTestFailedCode(int code, String error) {
    return 'Refused with $code: $error';
  }

  @override
  String channelTestFailed(String error) {
    return 'Did not get through: $error';
  }

  @override
  String channelLastDelivery(String when) {
    return 'last $when';
  }

  @override
  String get channelOff => 'off';

  @override
  String get channelEditOnWeb =>
      'Channels are created and edited on the web; here you can check one still works.';

  @override
  String get sessionsTitle => 'Signed in on';

  @override
  String get sessionsThisDevice => 'this device';

  @override
  String get sessionsBrowser => 'browser';

  @override
  String sessionsLastSeen(String when) {
    return 'last used $when';
  }

  @override
  String sessionsExpires(String when) {
    return 'expires $when';
  }

  @override
  String get sessionsEnd => 'End';

  @override
  String get sessionsEndTitle => 'End this session?';

  @override
  String get sessionsEndBody =>
      'Whatever is signed in there is signed out. If it is a device you no longer have, this is the thing to do.';

  @override
  String get sessionsNone => 'No other session.';

  @override
  String sessionsFailed(String detail) {
    return 'The sessions could not be read: $detail';
  }

  @override
  String get logsService => 'Service';

  @override
  String get logsScopedTrace => 'Logs of this request';

  @override
  String get logsScopedPod => 'Logs of this pod';

  @override
  String get logsScopedContainer => 'Logs of this container';

  @override
  String get logsScopedAll =>
      'Every level, because a request\'s logs are all of them.';

  @override
  String get logsOpenForTrace => 'Logs';

  @override
  String get logsEmptyScoped => 'Nothing was logged for this.';

  @override
  String get navMutes => 'Mutes';

  @override
  String get mutesEmpty => 'Nothing is silenced.';

  @override
  String get mutesSearch => 'Search mutes';

  @override
  String get mutesActive => 'silencing now';

  @override
  String mutesUpcoming(String when) {
    return 'starts $when';
  }

  @override
  String mutesUntil(String when) {
    return 'until $when';
  }

  @override
  String get mutesEnd => 'End now';

  @override
  String get mutesEndTitle => 'End this mute?';

  @override
  String get mutesEndBody =>
      'Alerting starts again immediately for whatever this was silencing.';

  @override
  String get mutesNew => 'Silence alerting';

  @override
  String get mutesNewName => 'Why';

  @override
  String get mutesNewNameHint => 'e.g. deploying checkout';

  @override
  String get mutesFor => 'for';

  @override
  String get mutesCreate => 'Silence';

  @override
  String get mutesAllRules => 'every rule';

  @override
  String mutesSomeRules(int count) {
    return '$count rules';
  }

  @override
  String get mutesRecurring => 'recurring; edited on the web';

  @override
  String get mutesDuration30m => '30 min';

  @override
  String get mutesDuration1h => '1 hour';

  @override
  String get mutesDuration2h => '2 hours';

  @override
  String get mutesDuration4h => '4 hours';

  @override
  String get rightNow => 'any moment';

  @override
  String inMinutes(int count) {
    return 'in $count min';
  }

  @override
  String inHours(int count) {
    return 'in $count h';
  }

  @override
  String inDays(int count) {
    return 'in $count days';
  }

  @override
  String get navRoutes => 'Routing';

  @override
  String get routesEmpty =>
      'No routing rule; every incident goes to the rule\'s own channels.';

  @override
  String get routesOrder =>
      'Tried in this order; the first match wins. Drag to reorder.';

  @override
  String get routesDefault => 'default';

  @override
  String get routesOff => 'off';

  @override
  String routesChannels(int count) {
    return '$count channels';
  }

  @override
  String get routesMatchAll => 'matches everything';

  @override
  String routesSeverities(String list) {
    return 'severity $list';
  }

  @override
  String routesServices(String list) {
    return 'service $list';
  }

  @override
  String routesTypes(String list) {
    return 'type $list';
  }

  @override
  String routesLabels(int count) {
    return '$count label conditions';
  }

  @override
  String routesWindow(String start, String end, String tz) {
    return '$start–$end $tz';
  }

  @override
  String get routesEditOnWeb =>
      'Conditions and channels are edited on the web.';

  @override
  String get calendarsTitle => 'Holiday calendars';

  @override
  String get calendarsAbout =>
      'Named lists of dates a recurring mute skips, such as public holidays.';

  @override
  String get calendarsEmpty => 'No holiday calendars.';

  @override
  String get calendarsNew => 'New holiday calendar';

  @override
  String get calendarsName => 'Name';

  @override
  String get calendarsDescription => 'Description';

  @override
  String get calendarsDates => 'Dates';

  @override
  String get calendarsDatesHint =>
      'One per line: YYYY-MM-DD for one date, MM-DD for every year.';

  @override
  String calendarsInvalid(String list) {
    return 'Invalid dates: $list';
  }

  @override
  String calendarsCount(int count) {
    return '$count dates';
  }

  @override
  String calendarsSummary(int count, int mutes) {
    return '$count dates, used by $mutes mutes';
  }

  @override
  String get calendarsCreate => 'Create';

  @override
  String get calendarsSave => 'Save';

  @override
  String get calendarsEdit => 'Edit';

  @override
  String get calendarsDelete => 'Delete';

  @override
  String calendarsDeleteTitle(String name) {
    return 'Delete $name?';
  }

  @override
  String get calendarsDeleteBody => 'The calendar goes; no mute uses it.';

  @override
  String calendarsDeleteInUse(int count) {
    return '$count mutes use this calendar; the server refuses to delete it while they do.';
  }

  @override
  String get mutesCalendars => 'Holiday calendars';

  @override
  String get deliveriesTitle => 'Delivery log';

  @override
  String deliveriesFor(String channel) {
    return '$channel deliveries';
  }

  @override
  String get deliveriesEmpty => 'No deliveries with this filter.';

  @override
  String get deliveriesAll => 'All';

  @override
  String get deliveriesOpen => 'Delivery log';

  @override
  String deliveryForRule(String rule) {
    return 'rule: $rule';
  }

  @override
  String deliveryAttempt(int attempt, String status, int ms) {
    return 'Attempt $attempt: $status, $ms ms';
  }

  @override
  String get deliveryAttemptOk => 'succeeded';

  @override
  String get deliveryAttemptFailed => 'failed';

  @override
  String get templatesTitle => 'New rule';

  @override
  String get templatesAll => 'All';

  @override
  String get templatesHosts => 'Hosts';

  @override
  String get templatesContainers => 'Containers';

  @override
  String get templatesApm => 'APM';

  @override
  String get templatesIntegrations => 'Integrations';

  @override
  String get templatesKubernetes => 'Kubernetes';

  @override
  String get templatesEmpty => 'No ready-made rules in this category.';

  @override
  String get templatesSetUp => 'Set up';

  @override
  String get templatesPreview => 'Preview';

  @override
  String get templatesCreate => 'Create rule';

  @override
  String templatesCreated(String name) {
    return '$name created.';
  }

  @override
  String templatesWouldFire(int count) {
    return 'Would have fired $count times in the last 6 hours.';
  }

  @override
  String get templatesNoData =>
      'No data for this rule to look at in the last 6 hours.';

  @override
  String templatesThreshold(String value) {
    return 'Threshold: $value';
  }

  @override
  String templatesOneSeries(int count) {
    return 'The busiest of $count series is drawn.';
  }

  @override
  String templatesReference(String metric, String value) {
    return '$metric is $value now; the threshold is a ratio of it.';
  }

  @override
  String get templatesRequired => 'Required.';

  @override
  String get templatesNumber => 'Enter a number.';

  @override
  String templatesRange(String min, String max) {
    return 'Must be between $min and $max.';
  }

  @override
  String get templatesSeconds => 's';

  @override
  String get templatesPerSecond => '/s';

  @override
  String get rulesNew => 'New rule';

  @override
  String templatesUnavailable(String list) {
    return 'Rule types this installation cannot use: $list';
  }

  @override
  String templatesPercent(String value) {
    return '$value%';
  }

  @override
  String get navErrors => 'Errors';

  @override
  String get errorsSearch => 'Search errors';

  @override
  String get errorsSort => 'Sort';

  @override
  String get errorsSortCount => 'By count';

  @override
  String get errorsSortLastSeen => 'By last seen';

  @override
  String get errorsSortFirstSeen => 'By first seen';

  @override
  String get errorsAll => 'All';

  @override
  String get errorsUnresolved => 'Unresolved';

  @override
  String get errorsResolved => 'Resolved';

  @override
  String get errorsIgnored => 'Ignored';

  @override
  String get errorsRegressed => 'Regressed';

  @override
  String get errorsNoWorkflow =>
      'This installation has no error workflow (it needs PostgreSQL); every group reads as unresolved.';

  @override
  String errorsCount(int count) {
    return '$count times';
  }

  @override
  String errorsCountOfTotal(int count, int total) {
    return '$count in this range, $total within retention';
  }

  @override
  String errorsFirstSeen(String when) {
    return 'first $when';
  }

  @override
  String errorsResolvedIn(String version) {
    return 'resolved in $version';
  }

  @override
  String errorsRegressions(int count) {
    return 'came back $count times';
  }

  @override
  String get errorsLastTrace => 'Open the last trace';

  @override
  String get errorsResolve => 'Resolve';

  @override
  String get errorsResolveInVersion => 'Resolve in version…';

  @override
  String get errorsVersion => 'Version';

  @override
  String get errorsVersionHint =>
      'Seen again after this version and the group reopens by itself.';

  @override
  String get errorsIgnore => 'Ignore';

  @override
  String get errorsReopen => 'Reopen';

  @override
  String get errorsComments => 'Comments';

  @override
  String get errorsNoComments => 'No comments.';

  @override
  String get errorsComment => 'Comment';

  @override
  String get errorsCommentTooLong => 'The comment is too long.';

  @override
  String get errorsSend => 'Send';

  @override
  String get errorsDeleteComment => 'Delete the comment';

  @override
  String get agentsTitle => 'Agent versions';

  @override
  String get agentsSearch => 'Search service, agent or version';

  @override
  String get agentsEmpty => 'No agents with this filter.';

  @override
  String get agentsOutdatedOnly => 'Behind only';

  @override
  String agentsLatest(String version, String channel) {
    return 'Latest release $version ($channel)';
  }

  @override
  String get agentsNoCatalog =>
      'The release catalog could not be read; statuses are unknown.';

  @override
  String get agentsOk => 'current';

  @override
  String get agentsOutdated => 'outdated';

  @override
  String get agentsUnsupported => 'unsupported';

  @override
  String get agentsThirdParty => 'third party';

  @override
  String get agentsUnknown => 'unknown';

  @override
  String agentsInstances(int count) {
    return '$count instances';
  }

  @override
  String get servicesAgents => 'Agent versions';

  @override
  String get tracesFilters => 'Filters';

  @override
  String get tracesTransaction => 'Transaction';

  @override
  String get tracesMinMs => 'Min ms';

  @override
  String get tracesMaxMs => 'Max ms';

  @override
  String get tracesAttributes => 'Attributes';

  @override
  String get tracesAttributesHint =>
      'key=value, separated by spaces; at most 10.';

  @override
  String get tracesErrorsOnly => 'Errors only';

  @override
  String get tracesSearch => 'Search';

  @override
  String get serviceTabTraces => 'Traces';

  @override
  String get samplingTitle => 'Sampling';

  @override
  String get samplingOffHere =>
      'Tail sampling is off on this server; the policy is stored but not applied.';

  @override
  String get samplingDefault =>
      'No policy is stored; the server\'s default is shown.';

  @override
  String samplingUpdated(String when, String email) {
    return 'updated $when by $email';
  }

  @override
  String get samplingEnabled => 'Policy active';

  @override
  String get samplingEnabledHint =>
      'Off keeps every trace; the policy stays but drops nothing.';

  @override
  String get samplingBaseline => 'Baseline ratio';

  @override
  String get samplingBaselineHint =>
      'The share kept of traces that match no rule.';

  @override
  String get samplingMaxSpans => 'Max spans per second';

  @override
  String get samplingMaxSpansHint => 'Per sampler instance; 0 means unlimited.';

  @override
  String get samplingPercentRange => 'Enter a percentage between 0 and 100.';

  @override
  String get samplingRules => 'Rules';

  @override
  String get samplingOrder =>
      'Tried in order; the first matching rule decides the ratio. Drag to reorder.';

  @override
  String get samplingNoRules => 'No rules; every trace falls to the baseline.';

  @override
  String get samplingAddRule => 'Add rule';

  @override
  String get samplingEditRule => 'Edit rule';

  @override
  String get samplingRuleName => 'Name';

  @override
  String get samplingRuleType => 'Type';

  @override
  String get samplingRuleRatio => 'Keep ratio';

  @override
  String get samplingRuleThreshold => 'Threshold (ms)';

  @override
  String get samplingRuleServices => 'Services';

  @override
  String get samplingRuleServicesHint => 'Comma separated.';

  @override
  String get samplingRuleService => 'Service (optional)';

  @override
  String get samplingRuleServiceHint =>
      'Filled in, the rule looks only at this service\'s spans.';

  @override
  String get samplingRuleRoute => 'Route';

  @override
  String get samplingRuleRouteHint =>
      'Glob on http.route; a trailing * is a prefix match.';

  @override
  String get samplingRuleKey => 'Attribute key';

  @override
  String get samplingRuleValue => 'Value (optional)';

  @override
  String get samplingRuleValueHint =>
      'Left empty, the key only has to be present.';

  @override
  String get samplingTypeError => 'failed traces';

  @override
  String get samplingTypeLatency => 'slow traces';

  @override
  String get samplingTypeService => 'by service';

  @override
  String get samplingTypeRoute => 'by route';

  @override
  String get samplingTypeAttribute => 'by attribute';

  @override
  String samplingOverMs(int ms) {
    return 'over $ms ms';
  }

  @override
  String samplingKeepRatio(String percent) {
    return 'keeps $percent%';
  }

  @override
  String samplingMatched(String percent) {
    return 'matched $percent% of traces in the last estimate';
  }

  @override
  String get samplingEstimate => 'Estimate';

  @override
  String get samplingSave => 'Save';

  @override
  String samplingKeeps(String traces, String spans) {
    return '$traces% of traces and $spans% of spans would be kept.';
  }

  @override
  String samplingExamined(int count, int minutes) {
    return '$count traces in the last $minutes minutes were examined.';
  }

  @override
  String get samplingNoRateLimit =>
      'The rate limit is not part of this estimate.';

  @override
  String get samplingConflict =>
      'Somebody else saved while you were editing; reload and try again.';

  @override
  String get samplingUnavailable =>
      'This installation cannot store a sampling policy.';

  @override
  String get serviceTabMap => 'Map';

  @override
  String get mapEmpty => 'No calls into or out of this service in this range.';

  @override
  String get mapIncoming => 'Callers';

  @override
  String get mapIncomingHint => 'Services that send requests to this one.';

  @override
  String get mapOutgoing => 'Dependencies';

  @override
  String get mapOutgoingHint =>
      'Services, databases and external addresses this one depends on.';

  @override
  String get mapKindDb => 'database';

  @override
  String get mapKindExternal => 'external';

  @override
  String get mapKindMessaging => 'queue';

  @override
  String mapCalls(int count) {
    return '$count calls';
  }

  @override
  String mapErrorRate(String percent) {
    return '$percent% errors';
  }

  @override
  String mapP95(String ms) {
    return 'p95 $ms ms';
  }

  @override
  String mapAvg(String ms) {
    return 'avg $ms ms';
  }

  @override
  String get mapOnPath => 'on this path';

  @override
  String mapPathOf(String transaction, int count) {
    return 'The dependencies $count traces of $transaction go through are marked.';
  }

  @override
  String get mapPathClear => 'Clear the transaction';

  @override
  String get mapShowPath => 'Show this transaction\'s path';

  @override
  String get navTemplates => 'Templates';

  @override
  String get navIncidents => 'Incidents';

  @override
  String get serviceTabTransactions => 'Transactions';

  @override
  String get serviceTabDatabases => 'Databases';

  @override
  String get transactionsEmpty => 'No transactions in this range.';

  @override
  String get serviceDatabasesEmpty => 'No database queries in this range.';

  @override
  String transactionsShare(String percent) {
    return '$percent% of the time';
  }

  @override
  String get sortTimeConsumed => 'Time consumed';

  @override
  String get sortThroughput => 'Throughput';

  @override
  String get sortCalls => 'Calls';

  @override
  String get sortSlowest => 'Slowest';

  @override
  String get sortErrors => 'Errors';

  @override
  String get deploymentsTitle => 'Deployments';

  @override
  String get deploymentsEmpty => 'No version changes in this range.';

  @override
  String get deploymentsRollback => 'rollback';

  @override
  String get deploymentsFirst => 'first version';

  @override
  String deploymentsWindow(int minutes) {
    return '$minutes minutes before and after';
  }

  @override
  String deploymentsNewErrors(int count) {
    return '$count error groups first seen after this deployment';
  }

  @override
  String get serviceLatencyChart => 'Latency (p95)';

  @override
  String get serviceApdexChart => 'Apdex';

  @override
  String get serviceTopTransactions => 'Top transactions by time consumed';

  @override
  String get serviceAllTransactions => 'All transactions';

  @override
  String get serviceHosts => 'Hosts';

  @override
  String get serviceContainers => 'Containers';

  @override
  String get servicePods => 'Pods';

  @override
  String get apdexTitle => 'Apdex threshold';

  @override
  String get apdexExplain =>
      'Requests up to this duration count as satisfied; up to four times it, half satisfied.';

  @override
  String get apdexThreshold => 'Milliseconds';

  @override
  String apdexDefault(int ms) {
    return 'Apdex $ms ms (default)';
  }

  @override
  String apdexSet(int ms) {
    return 'Apdex $ms ms';
  }

  @override
  String get apdexUnavailable =>
      'This installation cannot store service settings.';

  @override
  String get serviceUnknownHost =>
      'No agent data for this one; there is no page to open.';

  @override
  String get errorsLastMessage => 'Last message';

  @override
  String get errorsStacktrace => 'Stack trace';

  @override
  String get errorsNoStack => 'No stack trace for this group.';

  @override
  String errorsSymbolicated(int count) {
    return '$count frames resolved with a source map.';
  }

  @override
  String get errorsAffected => 'Affected';

  @override
  String get errorsAffectedVersions => 'Versions';

  @override
  String get errorsAffectedHosts => 'Hosts';

  @override
  String get errorsAffectedContainers => 'Containers';

  @override
  String get errorsAffectedTransactions => 'Transactions';

  @override
  String get errorsSamples => 'Sample requests';

  @override
  String get errorsNoSamples => 'No sample requests were kept.';

  @override
  String get errorsActivity => 'History';

  @override
  String get hostApmServices => 'Services sending traces';

  @override
  String get settingsProfile => 'Profile';

  @override
  String get settingsSecurity => 'Security';

  @override
  String get profileLanguage => 'Language';

  @override
  String get profileLanguageHint =>
      'The language the server writes in: alert e-mails and generated rule names. The app\'s own language follows the phone.';

  @override
  String get profileLanguageAuto => 'Automatic';

  @override
  String get securityPassword => 'Password';

  @override
  String get securityPasswordHint =>
      'Changing the password ends your other sessions; this device stays signed in.';

  @override
  String get securityCurrentPassword => 'Current password';

  @override
  String get securityNewPassword => 'New password';

  @override
  String securityMinLength(int count) {
    return 'At least $count characters.';
  }

  @override
  String get securityChangePassword => 'Change password';

  @override
  String get securityPasswordChanged =>
      'The password changed; the other sessions were ended.';

  @override
  String get settingsMembers => 'Members';

  @override
  String get membersTitle => 'Members';

  @override
  String get membersRole => 'Role';

  @override
  String get membersYou => 'you';

  @override
  String membersJoined(String when) {
    return 'joined $when';
  }

  @override
  String get membersRemove => 'Remove';

  @override
  String get membersRemoveTitle => 'Remove the member?';

  @override
  String membersRemoveBody(String email) {
    return '$email loses access to this organization.';
  }

  @override
  String get membersLeave => 'Leave';

  @override
  String get membersLeaveTitle => 'Leave the organization?';

  @override
  String membersLeaveBody(String org) {
    return 'You lose access to $org; getting back in takes a new invitation.';
  }

  @override
  String get membersForbidden => 'Your role does not allow managing members.';

  @override
  String get invitationsTitle => 'Invitations';

  @override
  String get invitationsEmpty => 'No pending invitations.';

  @override
  String get invitationsNew => 'New invitation';

  @override
  String get invitationsEmail => 'E-mail';

  @override
  String get invitationsSend => 'Send invitation';

  @override
  String get invitationsResend => 'Renew';

  @override
  String get invitationsRevoke => 'Revoke the invitation';

  @override
  String get invitationsExpired => 'expired';

  @override
  String invitationsExpires(String when) {
    return 'expires $when';
  }

  @override
  String invitationsSentTo(String email) {
    return 'An invitation e-mail went to $email.';
  }

  @override
  String invitationsNotSent(String email) {
    return 'The invitation for $email was created but no e-mail could be sent; pass the link on yourself.';
  }

  @override
  String get invitationsTokenOnce => 'This code is shown only once.';

  @override
  String get invitationsDone => 'Done';

  @override
  String invitationsExpiredAt(String when) {
    return 'ended $when';
  }

  @override
  String get settingsLicenseKeys => 'License keys';

  @override
  String get settingsApiKeys => 'API keys';

  @override
  String get settingsBrowserKeys => 'Browser keys';

  @override
  String get keysName => 'Name';

  @override
  String get keysCreate => 'Create';

  @override
  String keysCreated(String name) {
    return '$name was created.';
  }

  @override
  String get keysShownOnce => 'This value is shown only once.';

  @override
  String get keysImported =>
      'The value was supplied from outside, so there is nothing to show.';

  @override
  String get keysRevoke => 'Revoke';

  @override
  String get keysRevoked => 'revoked';

  @override
  String keysRevokeTitle(String name) {
    return 'Revoke $name?';
  }

  @override
  String keysLastUsed(String when) {
    return 'last used $when';
  }

  @override
  String get keysNeverUsed => 'never used';

  @override
  String keysExpires(String when) {
    return 'expires $when';
  }

  @override
  String get keysForbidden => 'Your role does not allow managing these keys.';

  @override
  String get licenseKeysEmpty => 'No license keys.';

  @override
  String get licenseKeysNew => 'New license key';

  @override
  String get licenseKeysRevokeBody =>
      'Agents sending data with this key stop. The server may keep accepting it for a short while, until its auth cache expires.';

  @override
  String get apiKeysEmpty => 'No API keys.';

  @override
  String get apiKeysNew => 'New API key';

  @override
  String get apiKeysViewerOnly => 'Only admins can create a key that writes.';

  @override
  String get apiKeysRevokeBody =>
      'Scripts using this key stop working immediately.';

  @override
  String get browserKeysEmpty => 'No browser keys.';

  @override
  String get browserKeysNew => 'New browser key';

  @override
  String get browserKeysService => 'Service name';

  @override
  String get browserKeysKindBrowser => 'Browser';

  @override
  String get browserKeysKindMobile => 'Mobile';

  @override
  String get browserKeysOriginsLabel => 'Allowed origins';

  @override
  String get browserKeysOriginsHint => 'One per line: https://app.example.com';

  @override
  String get browserKeysAppIds => 'Allowed app ids';

  @override
  String get browserKeysAppIdsHint => 'One per line: com.example.shop';

  @override
  String browserKeysOrigins(int count) {
    return '$count origins';
  }

  @override
  String browserKeysApps(int count) {
    return '$count apps';
  }

  @override
  String get browserKeysRevokeBody =>
      'Pages and apps sending data with this key stop.';

  @override
  String get settingsSourceMaps => 'Source maps';

  @override
  String get settingsAuditLog => 'Audit log';

  @override
  String get sourceMapsEmpty => 'No source maps stored.';

  @override
  String get sourceMapsUploadElsewhere =>
      'Uploading happens where the file is: the build machine or CI.';

  @override
  String sourceMapsSize(String kb) {
    return '$kb KB';
  }

  @override
  String get sourceMapsDeleteBody =>
      'Browser stacks of the build this file belongs to stop being un-minified.';

  @override
  String get auditActor => 'Who';

  @override
  String get auditAction => 'Action';

  @override
  String get auditActionHint => 'A prefix: member. or member.remove';

  @override
  String get auditEmpty => 'No entries with this filter.';

  @override
  String get auditMore => 'Older';

  @override
  String get auditEnd => 'End of the log.';

  @override
  String get auditUnknownActor => 'unknown';

  @override
  String get auditWithKey => 'with an API key';

  @override
  String get auditForbidden =>
      'Your role does not allow reading the audit log.';

  @override
  String get settingsOrganization => 'Organization';

  @override
  String get orgName => 'Name';

  @override
  String get orgRename => 'Rename';

  @override
  String get orgCreated => 'Created';

  @override
  String get orgTenantId => 'Tenant id';

  @override
  String get orgId => 'Organization id';

  @override
  String get orgLanguage => 'Organization language';

  @override
  String get orgLanguageHint =>
      'The language the server writes in for people who have not chosen one.';

  @override
  String get orgLanguageNone => 'Not chosen';

  @override
  String get orgForbidden =>
      'Your role does not allow changing the organization.';

  @override
  String get settingsSampling => 'APM sampling';

  @override
  String get settingsUsage => 'Usage and plan';

  @override
  String get usageCurrent => 'This period';

  @override
  String get usagePrevious => 'Previous period';

  @override
  String get usageSaas => 'SaaS';

  @override
  String get usageSelfHosted => 'Self-hosted';

  @override
  String get usagePlanDefault =>
      'No plan assigned; the catalog default applies.';

  @override
  String get usageBlocked => 'Ingest is blocked: the plan limit was exceeded.';

  @override
  String get usageIngest => 'Ingest';

  @override
  String get usageHosts => 'Hosts';

  @override
  String get usageUsers => 'Users';

  @override
  String get usageContainers => 'Containers';

  @override
  String get usageServices => 'Services';

  @override
  String get usageQueries => 'Queries';

  @override
  String get usageStored => 'Stored (compressed)';

  @override
  String get usageUnlimited => 'Unlimited';

  @override
  String usageProjected(String value) {
    return 'Projected at period end: $value';
  }

  @override
  String usageProjectedPercent(String value, int percent) {
    return 'Projected at period end: $value ($percent% of the limit)';
  }

  @override
  String get usageBySignal => 'By signal';

  @override
  String get usageRetention => 'Retention';

  @override
  String usageDays(int count) {
    return '$count days';
  }

  @override
  String get usageForbidden => 'Your role does not allow seeing usage.';

  @override
  String get settingsStorage => 'Storage';

  @override
  String get storageNotMeasured => 'The disks have not been measured yet.';

  @override
  String storageLevels(int warn, int high) {
    return 'Warning at $warn%, high at $high%.';
  }

  @override
  String storageFree(String free, String total) {
    return '$free free of $total';
  }

  @override
  String get storageBroken => 'The disk cannot be read.';

  @override
  String get storageForbidden => 'Your role does not allow seeing disk status.';

  @override
  String get settingsSso => 'SSO';

  @override
  String get ssoUnavailable =>
      'SSO cannot be used: the server\'s public URL (OPENLOG_PUBLIC_URL) is not set.';

  @override
  String get ssoSecretsPlain =>
      'Secrets are stored unencrypted; set a key on the server.';

  @override
  String get ssoConnections => 'Connections';

  @override
  String get ssoNoConnections => 'No SSO connections.';

  @override
  String get ssoEditOnWeb =>
      'Creating and editing a connection is on the web: it means pasting a metadata URL, a client secret and a certificate.';

  @override
  String get ssoTestOk => 'The server-side checks passed.';

  @override
  String ssoTestFailed(String checks) {
    return 'Checks that failed: $checks';
  }

  @override
  String get ssoEnforce => 'SSO required';

  @override
  String get ssoEnforceHint =>
      'On, everybody signs in through the identity provider.';

  @override
  String ssoBreakGlass(int count) {
    return '$count people can still sign in with a password.';
  }

  @override
  String get ssoNoBreakGlass => 'Nobody is allowed to sign in with a password.';

  @override
  String get ssoDomains => 'Domains';

  @override
  String get ssoDomainsHint =>
      'E-mail addresses in these domains sign in through SSO.';

  @override
  String get ssoDomainAdd => 'Add a domain';

  @override
  String get ssoVerified => 'verified';

  @override
  String get ssoUnverified => 'unverified';

  @override
  String get ssoVerifyDns => 'Verify by DNS';

  @override
  String get ssoVerifyEmail => 'Verify by e-mail';

  @override
  String get ssoRoleMappings => 'Group mappings';

  @override
  String get ssoRoleMappingsHint =>
      'A group at the identity provider becomes a role here. Owner is not given by a mapping.';

  @override
  String get ssoGroup => 'Group';

  @override
  String get ssoScimTokens => 'SCIM tokens';

  @override
  String get ssoNoScimTokens => 'No SCIM tokens.';

  @override
  String get ssoForbidden =>
      'Your role does not allow seeing the SSO settings.';

  @override
  String get ssoTest => 'Test the connection';

  @override
  String get ssoHealthOk => 'Healthy';

  @override
  String get ssoHealthWarning => 'Warning';

  @override
  String get ssoHealthError => 'Error';

  @override
  String get ssoHealthUnknown => 'Not checked yet';

  @override
  String get privacySsoReauth =>
      'This account has no password; the server wants a recent single sign-on instead.';

  @override
  String get privacyDelete => 'Delete';

  @override
  String privacyUnverified(String email) {
    return '$email is not verified.';
  }

  @override
  String get privacyResend => 'Send the verification e-mail again';

  @override
  String get privacyVerificationSent => 'The verification e-mail went out.';

  @override
  String get privacyExport => 'A copy of my data';

  @override
  String get privacyExportHint =>
      'Downloaded from the web once it is ready; a phone is not where anybody opens an archive.';

  @override
  String get privacyExportRequest => 'Request a copy';

  @override
  String get privacyExportQueued =>
      'Requested; an e-mail arrives when it is ready.';

  @override
  String privacyOrgDeletion(String org, String when) {
    return '$org is scheduled for deletion: it goes permanently $when.';
  }

  @override
  String get privacyCancelDeletion => 'Cancel the deletion';

  @override
  String get privacyDangerous => 'Cannot be undone';

  @override
  String get privacyDeleteOrg => 'Delete the organization';

  @override
  String get privacyDeleteOrgTitle => 'Delete the organization';

  @override
  String privacyDeleteOrgBody(String org, int days) {
    return '$org and everything in it is deleted permanently after $days days. You can cancel until then. Type its name to confirm.';
  }

  @override
  String get privacyDeleteAccount => 'Delete my account';

  @override
  String get privacyDeleteAccountTitle => 'Delete the account';

  @override
  String get privacyDeleteAccountBody =>
      'Your account and your personal data are deleted. Type your e-mail address to confirm.';

  @override
  String get alreadyVerified => 'This address is already verified.';

  @override
  String get reauthNeeded =>
      'Prove it is you: the password was wrong, or the single sign-on is too old.';
}
