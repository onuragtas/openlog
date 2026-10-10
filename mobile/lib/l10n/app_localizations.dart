import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_localizations/flutter_localizations.dart';
import 'package:intl/intl.dart' as intl;

import 'app_localizations_en.dart';
import 'app_localizations_tr.dart';

// ignore_for_file: type=lint

/// Callers can lookup localized strings with an instance of L
/// returned by `L.of(context)`.
///
/// Applications need to include `L.delegate()` in their app's
/// `localizationDelegates` list, and the locales they support in the app's
/// `supportedLocales` list. For example:
///
/// ```dart
/// import 'l10n/app_localizations.dart';
///
/// return MaterialApp(
///   localizationsDelegates: L.localizationsDelegates,
///   supportedLocales: L.supportedLocales,
///   home: MyApplicationHome(),
/// );
/// ```
///
/// ## Update pubspec.yaml
///
/// Please make sure to update your pubspec.yaml to include the following
/// packages:
///
/// ```yaml
/// dependencies:
///   # Internationalization support.
///   flutter_localizations:
///     sdk: flutter
///   intl: any # Use the pinned version from flutter_localizations
///
///   # Rest of dependencies
/// ```
///
/// ## iOS Applications
///
/// iOS applications define key application metadata, including supported
/// locales, in an Info.plist file that is built into the application bundle.
/// To configure the locales supported by your app, you’ll need to edit this
/// file.
///
/// First, open your project’s ios/Runner.xcworkspace Xcode workspace file.
/// Then, in the Project Navigator, open the Info.plist file under the Runner
/// project’s Runner folder.
///
/// Next, select the Information Property List item, select Add Item from the
/// Editor menu, then select Localizations from the pop-up menu.
///
/// Select and expand the newly-created Localizations item then, for each
/// locale your application supports, add a new item and select the locale
/// you wish to add from the pop-up menu in the Value field. This list should
/// be consistent with the languages listed in the L.supportedLocales
/// property.
abstract class L {
  L(String locale)
    : localeName = intl.Intl.canonicalizedLocale(locale.toString());

  final String localeName;

  static L of(BuildContext context) {
    return Localizations.of<L>(context, L)!;
  }

  static const LocalizationsDelegate<L> delegate = _LDelegate();

  /// A list of this localizations delegate along with the default localizations
  /// delegates.
  ///
  /// Returns a list of localizations delegates containing this delegate along with
  /// GlobalMaterialLocalizations.delegate, GlobalCupertinoLocalizations.delegate,
  /// and GlobalWidgetsLocalizations.delegate.
  ///
  /// Additional delegates can be added by appending to this list in
  /// MaterialApp. This list does not have to be used at all if a custom list
  /// of delegates is preferred or required.
  static const List<LocalizationsDelegate<dynamic>> localizationsDelegates =
      <LocalizationsDelegate<dynamic>>[
        delegate,
        GlobalMaterialLocalizations.delegate,
        GlobalCupertinoLocalizations.delegate,
        GlobalWidgetsLocalizations.delegate,
      ];

  /// A list of this localizations delegate's supported locales.
  static const List<Locale> supportedLocales = <Locale>[
    Locale('en'),
    Locale('tr'),
  ];

  /// No description provided for @appTitle.
  ///
  /// In en, this message translates to:
  /// **'openlog'**
  String get appTitle;

  /// No description provided for @serverTitle.
  ///
  /// In en, this message translates to:
  /// **'Connect to openlog'**
  String get serverTitle;

  /// No description provided for @serverDescription.
  ///
  /// In en, this message translates to:
  /// **'openlog runs on your own server. The address below is the hosted one — replace it with yours if you run your own.'**
  String get serverDescription;

  /// No description provided for @serverAddressLabel.
  ///
  /// In en, this message translates to:
  /// **'Server address'**
  String get serverAddressLabel;

  /// No description provided for @serverContinue.
  ///
  /// In en, this message translates to:
  /// **'Continue'**
  String get serverContinue;

  /// No description provided for @serverChecking.
  ///
  /// In en, this message translates to:
  /// **'Checking…'**
  String get serverChecking;

  /// No description provided for @serverInvalidAddress.
  ///
  /// In en, this message translates to:
  /// **'That does not look like an address.'**
  String get serverInvalidAddress;

  /// No description provided for @serverUnreachable.
  ///
  /// In en, this message translates to:
  /// **'Cannot reach {address}. Check the address and your connection.'**
  String serverUnreachable(String address);

  /// No description provided for @serverNotOpenlog.
  ///
  /// In en, this message translates to:
  /// **'That address answered, but not like an openlog server.'**
  String get serverNotOpenlog;

  /// No description provided for @serverChange.
  ///
  /// In en, this message translates to:
  /// **'Change server'**
  String get serverChange;

  /// No description provided for @signInTitle.
  ///
  /// In en, this message translates to:
  /// **'Sign in to openlog'**
  String get signInTitle;

  /// No description provided for @signInDescription.
  ///
  /// In en, this message translates to:
  /// **'Use the e-mail address and password of your openlog account.'**
  String get signInDescription;

  /// No description provided for @emailLabel.
  ///
  /// In en, this message translates to:
  /// **'E-mail'**
  String get emailLabel;

  /// No description provided for @emailHint.
  ///
  /// In en, this message translates to:
  /// **'you@example.com'**
  String get emailHint;

  /// No description provided for @passwordLabel.
  ///
  /// In en, this message translates to:
  /// **'Password'**
  String get passwordLabel;

  /// No description provided for @signInSubmit.
  ///
  /// In en, this message translates to:
  /// **'Sign in'**
  String get signInSubmit;

  /// No description provided for @signInChecking.
  ///
  /// In en, this message translates to:
  /// **'Signing in…'**
  String get signInChecking;

  /// No description provided for @signInInvalid.
  ///
  /// In en, this message translates to:
  /// **'Wrong e-mail address or password.'**
  String get signInInvalid;

  /// No description provided for @signInRateLimited.
  ///
  /// In en, this message translates to:
  /// **'Too many failed attempts. Wait a few minutes and try again.'**
  String get signInRateLimited;

  /// No description provided for @signInRequired.
  ///
  /// In en, this message translates to:
  /// **'E-mail address and password are required.'**
  String get signInRequired;

  /// No description provided for @signInStaticMode.
  ///
  /// In en, this message translates to:
  /// **'This server runs with OPENLOG_AUTH_MODE=static and has no user accounts.'**
  String get signInStaticMode;

  /// No description provided for @deviceNameLabel.
  ///
  /// In en, this message translates to:
  /// **'Device name'**
  String get deviceNameLabel;

  /// No description provided for @deviceNameHelp.
  ///
  /// In en, this message translates to:
  /// **'Shown in your session list, so you can tell this device from your others and sign it out.'**
  String get deviceNameHelp;

  /// No description provided for @deviceNameRequired.
  ///
  /// In en, this message translates to:
  /// **'A device name is required.'**
  String get deviceNameRequired;

  /// No description provided for @signUpPrompt.
  ///
  /// In en, this message translates to:
  /// **'No account?'**
  String get signUpPrompt;

  /// No description provided for @signUpLink.
  ///
  /// In en, this message translates to:
  /// **'Create an account'**
  String get signUpLink;

  /// No description provided for @signUpTitle.
  ///
  /// In en, this message translates to:
  /// **'Create your openlog account'**
  String get signUpTitle;

  /// No description provided for @signUpDescription.
  ///
  /// In en, this message translates to:
  /// **'Start a new organization. You become its owner and can invite your team.'**
  String get signUpDescription;

  /// No description provided for @nameLabel.
  ///
  /// In en, this message translates to:
  /// **'Your name'**
  String get nameLabel;

  /// No description provided for @orgLabel.
  ///
  /// In en, this message translates to:
  /// **'Organization name'**
  String get orgLabel;

  /// No description provided for @orgHint.
  ///
  /// In en, this message translates to:
  /// **'e.g. Acme Inc.'**
  String get orgHint;

  /// No description provided for @passwordHintMin.
  ///
  /// In en, this message translates to:
  /// **'At least {min} characters, and not your e-mail address.'**
  String passwordHintMin(int min);

  /// No description provided for @signUpSubmit.
  ///
  /// In en, this message translates to:
  /// **'Create account'**
  String get signUpSubmit;

  /// No description provided for @signUpSubmitting.
  ///
  /// In en, this message translates to:
  /// **'Creating the account…'**
  String get signUpSubmitting;

  /// No description provided for @signUpRequired.
  ///
  /// In en, this message translates to:
  /// **'Organization name, e-mail address and password are required.'**
  String get signUpRequired;

  /// No description provided for @signUpDisabled.
  ///
  /// In en, this message translates to:
  /// **'Sign-up is closed on this server. Ask an administrator of your organization for an invitation.'**
  String get signUpDisabled;

  /// No description provided for @signUpEmailTaken.
  ///
  /// In en, this message translates to:
  /// **'An account with this e-mail address already exists. Sign in instead.'**
  String get signUpEmailTaken;

  /// No description provided for @signUpRateLimited.
  ///
  /// In en, this message translates to:
  /// **'Too many sign-up attempts. Try again later.'**
  String get signUpRateLimited;

  /// No description provided for @signUpOnWeb.
  ///
  /// In en, this message translates to:
  /// **'This server asks for a CAPTCHA when signing up, which this app cannot show. Create the account at {address} in a browser, then sign in here.'**
  String signUpOnWeb(String address);

  /// No description provided for @signUpVerificationNote.
  ///
  /// In en, this message translates to:
  /// **'We will e-mail you a link to confirm your address.'**
  String get signUpVerificationNote;

  /// No description provided for @haveAccount.
  ///
  /// In en, this message translates to:
  /// **'Already have an account?'**
  String get haveAccount;

  /// No description provided for @toSignIn.
  ///
  /// In en, this message translates to:
  /// **'Sign in'**
  String get toSignIn;

  /// No description provided for @homeSignedInAs.
  ///
  /// In en, this message translates to:
  /// **'Signed in as {email}'**
  String homeSignedInAs(String email);

  /// No description provided for @homeOrganization.
  ///
  /// In en, this message translates to:
  /// **'Organization'**
  String get homeOrganization;

  /// No description provided for @homeRole.
  ///
  /// In en, this message translates to:
  /// **'Role'**
  String get homeRole;

  /// No description provided for @homeSignOut.
  ///
  /// In en, this message translates to:
  /// **'Sign out'**
  String get homeSignOut;

  /// No description provided for @homeNextPhase.
  ///
  /// In en, this message translates to:
  /// **'Alerts, services and logs arrive in the next phase.'**
  String get homeNextPhase;

  /// No description provided for @homeSwitchOrganization.
  ///
  /// In en, this message translates to:
  /// **'Switch organization'**
  String get homeSwitchOrganization;

  /// No description provided for @roleOwner.
  ///
  /// In en, this message translates to:
  /// **'Owner'**
  String get roleOwner;

  /// No description provided for @roleAdmin.
  ///
  /// In en, this message translates to:
  /// **'Admin'**
  String get roleAdmin;

  /// No description provided for @roleMember.
  ///
  /// In en, this message translates to:
  /// **'Member'**
  String get roleMember;

  /// No description provided for @roleViewer.
  ///
  /// In en, this message translates to:
  /// **'Viewer'**
  String get roleViewer;

  /// No description provided for @roleUnknown.
  ///
  /// In en, this message translates to:
  /// **'Unknown role'**
  String get roleUnknown;

  /// No description provided for @retry.
  ///
  /// In en, this message translates to:
  /// **'Try again'**
  String get retry;

  /// No description provided for @cancel.
  ///
  /// In en, this message translates to:
  /// **'Cancel'**
  String get cancel;

  /// No description provided for @errorUnexpected.
  ///
  /// In en, this message translates to:
  /// **'Something went wrong: {detail}'**
  String errorUnexpected(String detail);

  /// No description provided for @alertsTitle.
  ///
  /// In en, this message translates to:
  /// **'Alerts'**
  String get alertsTitle;

  /// No description provided for @alertsEmpty.
  ///
  /// In en, this message translates to:
  /// **'Nothing is firing.'**
  String get alertsEmpty;

  /// No description provided for @alertsEmptyHint.
  ///
  /// In en, this message translates to:
  /// **'Open alerts show up here the moment a rule fires.'**
  String get alertsEmptyHint;

  /// No description provided for @alertsForbidden.
  ///
  /// In en, this message translates to:
  /// **'Your role does not allow reading alerts.'**
  String get alertsForbidden;

  /// No description provided for @alertsAlreadyResolved.
  ///
  /// In en, this message translates to:
  /// **'That alert resolved before it could be acknowledged.'**
  String get alertsAlreadyResolved;

  /// No description provided for @alertsAcknowledge.
  ///
  /// In en, this message translates to:
  /// **'Acknowledge'**
  String get alertsAcknowledge;

  /// No description provided for @alertsAcknowledgedBy.
  ///
  /// In en, this message translates to:
  /// **'Acknowledged by {email}'**
  String alertsAcknowledgedBy(String email);

  /// No description provided for @alertsAcknowledgedUnknown.
  ///
  /// In en, this message translates to:
  /// **'Acknowledged'**
  String get alertsAcknowledgedUnknown;

  /// No description provided for @alertsCounts.
  ///
  /// In en, this message translates to:
  /// **'{open} open, {acknowledged} acknowledged'**
  String alertsCounts(int open, int acknowledged);

  /// No description provided for @alertsResolvedRecently.
  ///
  /// In en, this message translates to:
  /// **'{count} resolved in the last 7 days'**
  String alertsResolvedRecently(int count);

  /// No description provided for @alertsMuted.
  ///
  /// In en, this message translates to:
  /// **'Muted'**
  String get alertsMuted;

  /// No description provided for @alertsFlapping.
  ///
  /// In en, this message translates to:
  /// **'Flapping'**
  String get alertsFlapping;

  /// No description provided for @alertsOpened.
  ///
  /// In en, this message translates to:
  /// **'Opened {when}'**
  String alertsOpened(String when);

  /// No description provided for @severityCritical.
  ///
  /// In en, this message translates to:
  /// **'Critical'**
  String get severityCritical;

  /// No description provided for @severityWarning.
  ///
  /// In en, this message translates to:
  /// **'Warning'**
  String get severityWarning;

  /// No description provided for @severityInfo.
  ///
  /// In en, this message translates to:
  /// **'Info'**
  String get severityInfo;

  /// No description provided for @severityUnknown.
  ///
  /// In en, this message translates to:
  /// **'Unknown severity'**
  String get severityUnknown;

  /// No description provided for @refresh.
  ///
  /// In en, this message translates to:
  /// **'Refresh'**
  String get refresh;

  /// No description provided for @justNow.
  ///
  /// In en, this message translates to:
  /// **'just now'**
  String get justNow;

  /// No description provided for @minutesAgo.
  ///
  /// In en, this message translates to:
  /// **'{count}m ago'**
  String minutesAgo(int count);

  /// No description provided for @hoursAgo.
  ///
  /// In en, this message translates to:
  /// **'{count}h ago'**
  String hoursAgo(int count);

  /// No description provided for @daysAgo.
  ///
  /// In en, this message translates to:
  /// **'{count}d ago'**
  String daysAgo(int count);

  /// No description provided for @navAlerts.
  ///
  /// In en, this message translates to:
  /// **'Alerts'**
  String get navAlerts;

  /// No description provided for @navLogs.
  ///
  /// In en, this message translates to:
  /// **'Logs'**
  String get navLogs;

  /// No description provided for @servicesEmpty.
  ///
  /// In en, this message translates to:
  /// **'No service has reported in the last hour.'**
  String get servicesEmpty;

  /// No description provided for @servicesSearch.
  ///
  /// In en, this message translates to:
  /// **'Search services'**
  String get servicesSearch;

  /// No description provided for @servicesForbidden.
  ///
  /// In en, this message translates to:
  /// **'Your role does not allow reading services.'**
  String get servicesForbidden;

  /// No description provided for @svcThroughput.
  ///
  /// In en, this message translates to:
  /// **'rpm'**
  String get svcThroughput;

  /// No description provided for @svcErrorRate.
  ///
  /// In en, this message translates to:
  /// **'errors'**
  String get svcErrorRate;

  /// No description provided for @svcP95.
  ///
  /// In en, this message translates to:
  /// **'p95'**
  String get svcP95;

  /// No description provided for @svcApdex.
  ///
  /// In en, this message translates to:
  /// **'Apdex'**
  String get svcApdex;

  /// No description provided for @svcNoData.
  ///
  /// In en, this message translates to:
  /// **'—'**
  String get svcNoData;

  /// No description provided for @logsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No log records match.'**
  String get logsEmpty;

  /// No description provided for @logsSearch.
  ///
  /// In en, this message translates to:
  /// **'Search in the message'**
  String get logsSearch;

  /// No description provided for @logsForbidden.
  ///
  /// In en, this message translates to:
  /// **'Your role does not allow reading logs.'**
  String get logsForbidden;

  /// No description provided for @logsSeverity.
  ///
  /// In en, this message translates to:
  /// **'Severity'**
  String get logsSeverity;

  /// No description provided for @logsSeverityAll.
  ///
  /// In en, this message translates to:
  /// **'All'**
  String get logsSeverityAll;

  /// No description provided for @logsNoService.
  ///
  /// In en, this message translates to:
  /// **'no service'**
  String get logsNoService;

  /// No description provided for @navDashboards.
  ///
  /// In en, this message translates to:
  /// **'Dashboards'**
  String get navDashboards;

  /// No description provided for @dashboardsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No dashboard yet.'**
  String get dashboardsEmpty;

  /// No description provided for @dashboardsSearch.
  ///
  /// In en, this message translates to:
  /// **'Search dashboards'**
  String get dashboardsSearch;

  /// No description provided for @dashboardsForbidden.
  ///
  /// In en, this message translates to:
  /// **'Your role does not allow reading dashboards.'**
  String get dashboardsForbidden;

  /// No description provided for @dashboardWidgets.
  ///
  /// In en, this message translates to:
  /// **'{count} widgets on {pages} pages'**
  String dashboardWidgets(int count, int pages);

  /// No description provided for @dashboardNoQuery.
  ///
  /// In en, this message translates to:
  /// **'Nothing to run'**
  String get dashboardNoQuery;

  /// No description provided for @dashboardWidgetFailed.
  ///
  /// In en, this message translates to:
  /// **'This widget\'s query did not answer.'**
  String get dashboardWidgetFailed;

  /// No description provided for @dashboardOnWeb.
  ///
  /// In en, this message translates to:
  /// **'Best read on the web'**
  String get dashboardOnWeb;

  /// No description provided for @dashboardNoData.
  ///
  /// In en, this message translates to:
  /// **'No data'**
  String get dashboardNoData;

  /// No description provided for @dashboardLoading.
  ///
  /// In en, this message translates to:
  /// **'Running the queries…'**
  String get dashboardLoading;

  /// No description provided for @navApm.
  ///
  /// In en, this message translates to:
  /// **'APM'**
  String get navApm;

  /// No description provided for @navSettings.
  ///
  /// In en, this message translates to:
  /// **'Settings'**
  String get navSettings;

  /// No description provided for @navMain.
  ///
  /// In en, this message translates to:
  /// **'Main navigation'**
  String get navMain;

  /// No description provided for @settingsAccount.
  ///
  /// In en, this message translates to:
  /// **'Account'**
  String get settingsAccount;

  /// No description provided for @closeMenu.
  ///
  /// In en, this message translates to:
  /// **'Close menu'**
  String get closeMenu;

  /// No description provided for @navHosts.
  ///
  /// In en, this message translates to:
  /// **'Hosts'**
  String get navHosts;

  /// No description provided for @navContainers.
  ///
  /// In en, this message translates to:
  /// **'Containers'**
  String get navContainers;

  /// No description provided for @navKubernetes.
  ///
  /// In en, this message translates to:
  /// **'Kubernetes'**
  String get navKubernetes;

  /// No description provided for @navDatabases.
  ///
  /// In en, this message translates to:
  /// **'Databases'**
  String get navDatabases;

  /// No description provided for @navSlos.
  ///
  /// In en, this message translates to:
  /// **'SLOs'**
  String get navSlos;

  /// No description provided for @navSynthetics.
  ///
  /// In en, this message translates to:
  /// **'Synthetics'**
  String get navSynthetics;

  /// No description provided for @navJobs.
  ///
  /// In en, this message translates to:
  /// **'Job monitoring'**
  String get navJobs;

  /// No description provided for @navVulnerabilities.
  ///
  /// In en, this message translates to:
  /// **'Vulnerabilities'**
  String get navVulnerabilities;

  /// No description provided for @sectionForbidden.
  ///
  /// In en, this message translates to:
  /// **'Your role does not allow reading this section.'**
  String get sectionForbidden;

  /// No description provided for @sectionSearch.
  ///
  /// In en, this message translates to:
  /// **'Search'**
  String get sectionSearch;

  /// No description provided for @hostsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No host is reporting.'**
  String get hostsEmpty;

  /// No description provided for @containersEmpty.
  ///
  /// In en, this message translates to:
  /// **'No container is reporting.'**
  String get containersEmpty;

  /// No description provided for @podsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No pod found.'**
  String get podsEmpty;

  /// No description provided for @databasesEmpty.
  ///
  /// In en, this message translates to:
  /// **'No database instance is reporting.'**
  String get databasesEmpty;

  /// No description provided for @slosEmpty.
  ///
  /// In en, this message translates to:
  /// **'No SLO defined.'**
  String get slosEmpty;

  /// No description provided for @syntheticsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No synthetic check defined.'**
  String get syntheticsEmpty;

  /// No description provided for @jobsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No job monitor defined.'**
  String get jobsEmpty;

  /// No description provided for @vulnerabilitiesEmpty.
  ///
  /// In en, this message translates to:
  /// **'No vulnerability found.'**
  String get vulnerabilitiesEmpty;

  /// No description provided for @statCpu.
  ///
  /// In en, this message translates to:
  /// **'CPU'**
  String get statCpu;

  /// No description provided for @statMemory.
  ///
  /// In en, this message translates to:
  /// **'Memory'**
  String get statMemory;

  /// No description provided for @statDisk.
  ///
  /// In en, this message translates to:
  /// **'Disk'**
  String get statDisk;

  /// No description provided for @statRestarts.
  ///
  /// In en, this message translates to:
  /// **'restarts'**
  String get statRestarts;

  /// No description provided for @statUptime.
  ///
  /// In en, this message translates to:
  /// **'uptime'**
  String get statUptime;

  /// No description provided for @statRuns.
  ///
  /// In en, this message translates to:
  /// **'runs'**
  String get statRuns;

  /// No description provided for @statFailures.
  ///
  /// In en, this message translates to:
  /// **'failures'**
  String get statFailures;

  /// No description provided for @statBudget.
  ///
  /// In en, this message translates to:
  /// **'budget left'**
  String get statBudget;

  /// No description provided for @statObjective.
  ///
  /// In en, this message translates to:
  /// **'objective'**
  String get statObjective;

  /// No description provided for @statScore.
  ///
  /// In en, this message translates to:
  /// **'score'**
  String get statScore;

  /// No description provided for @statHosts.
  ///
  /// In en, this message translates to:
  /// **'hosts'**
  String get statHosts;

  /// No description provided for @statCalls.
  ///
  /// In en, this message translates to:
  /// **'calls'**
  String get statCalls;

  /// No description provided for @stateDisabled.
  ///
  /// In en, this message translates to:
  /// **'Disabled'**
  String get stateDisabled;

  /// No description provided for @stateNotReporting.
  ///
  /// In en, this message translates to:
  /// **'Not reporting'**
  String get stateNotReporting;

  /// No description provided for @stateReady.
  ///
  /// In en, this message translates to:
  /// **'Ready'**
  String get stateReady;

  /// No description provided for @sloMet.
  ///
  /// In en, this message translates to:
  /// **'Met'**
  String get sloMet;

  /// No description provided for @sloBreached.
  ///
  /// In en, this message translates to:
  /// **'Breached'**
  String get sloBreached;

  /// No description provided for @detailGone.
  ///
  /// In en, this message translates to:
  /// **'That is no longer on the server.'**
  String get detailGone;

  /// No description provided for @incidentTimeline.
  ///
  /// In en, this message translates to:
  /// **'Timeline'**
  String get incidentTimeline;

  /// No description provided for @incidentNoEvents.
  ///
  /// In en, this message translates to:
  /// **'Nothing has happened to this incident yet.'**
  String get incidentNoEvents;

  /// No description provided for @incidentDeliveries.
  ///
  /// In en, this message translates to:
  /// **'Notifications'**
  String get incidentDeliveries;

  /// No description provided for @incidentNoDeliveries.
  ///
  /// In en, this message translates to:
  /// **'Nothing was sent for this incident.'**
  String get incidentNoDeliveries;

  /// No description provided for @incidentLabels.
  ///
  /// In en, this message translates to:
  /// **'Labels'**
  String get incidentLabels;

  /// No description provided for @incidentValue.
  ///
  /// In en, this message translates to:
  /// **'Value'**
  String get incidentValue;

  /// No description provided for @incidentThreshold.
  ///
  /// In en, this message translates to:
  /// **'Threshold'**
  String get incidentThreshold;

  /// No description provided for @incidentOpenService.
  ///
  /// In en, this message translates to:
  /// **'Open {name}'**
  String incidentOpenService(String name);

  /// No description provided for @incidentResolve.
  ///
  /// In en, this message translates to:
  /// **'Resolve'**
  String get incidentResolve;

  /// No description provided for @incidentResolveHint.
  ///
  /// In en, this message translates to:
  /// **'A series that is still breaching opens a new incident, so this does not silence anything.'**
  String get incidentResolveHint;

  /// No description provided for @incidentResolvedBy.
  ///
  /// In en, this message translates to:
  /// **'Resolved by {email}'**
  String incidentResolvedBy(String email);

  /// No description provided for @incidentResolved.
  ///
  /// In en, this message translates to:
  /// **'Resolved {when}'**
  String incidentResolved(String when);

  /// No description provided for @incidentNote.
  ///
  /// In en, this message translates to:
  /// **'Add a note'**
  String get incidentNote;

  /// No description provided for @incidentNoteHint.
  ///
  /// In en, this message translates to:
  /// **'What you found'**
  String get incidentNoteHint;

  /// No description provided for @incidentNoteSend.
  ///
  /// In en, this message translates to:
  /// **'Send'**
  String get incidentNoteSend;

  /// No description provided for @incidentEventOpened.
  ///
  /// In en, this message translates to:
  /// **'Opened'**
  String get incidentEventOpened;

  /// No description provided for @incidentEventFlapping.
  ///
  /// In en, this message translates to:
  /// **'Flapping'**
  String get incidentEventFlapping;

  /// No description provided for @incidentEventAcknowledged.
  ///
  /// In en, this message translates to:
  /// **'Acknowledged'**
  String get incidentEventAcknowledged;

  /// No description provided for @incidentEventNote.
  ///
  /// In en, this message translates to:
  /// **'Note'**
  String get incidentEventNote;

  /// No description provided for @incidentEventRenotified.
  ///
  /// In en, this message translates to:
  /// **'Re-notified'**
  String get incidentEventRenotified;

  /// No description provided for @incidentEventResolved.
  ///
  /// In en, this message translates to:
  /// **'Resolved'**
  String get incidentEventResolved;

  /// No description provided for @incidentEventDelivered.
  ///
  /// In en, this message translates to:
  /// **'Notification delivered'**
  String get incidentEventDelivered;

  /// No description provided for @incidentEventFailed.
  ///
  /// In en, this message translates to:
  /// **'Notification failed'**
  String get incidentEventFailed;

  /// No description provided for @incidentEventSuppressed.
  ///
  /// In en, this message translates to:
  /// **'Notification suppressed'**
  String get incidentEventSuppressed;

  /// No description provided for @incidentEventMuted.
  ///
  /// In en, this message translates to:
  /// **'Notification muted'**
  String get incidentEventMuted;

  /// No description provided for @incidentEventUnknown.
  ///
  /// In en, this message translates to:
  /// **'Event this app does not know'**
  String get incidentEventUnknown;

  /// No description provided for @serviceSignals.
  ///
  /// In en, this message translates to:
  /// **'Golden signals'**
  String get serviceSignals;

  /// No description provided for @serviceRequests.
  ///
  /// In en, this message translates to:
  /// **'Requests'**
  String get serviceRequests;

  /// No description provided for @serviceErrors.
  ///
  /// In en, this message translates to:
  /// **'Errors'**
  String get serviceErrors;

  /// No description provided for @serviceLatency.
  ///
  /// In en, this message translates to:
  /// **'Latency'**
  String get serviceLatency;

  /// No description provided for @serviceNoData.
  ///
  /// In en, this message translates to:
  /// **'This service has not reported in the window.'**
  String get serviceNoData;

  /// No description provided for @serviceThroughputChart.
  ///
  /// In en, this message translates to:
  /// **'Requests per minute'**
  String get serviceThroughputChart;

  /// No description provided for @serviceErrorRateChart.
  ///
  /// In en, this message translates to:
  /// **'Error rate'**
  String get serviceErrorRateChart;

  /// No description provided for @deliveryPending.
  ///
  /// In en, this message translates to:
  /// **'Pending'**
  String get deliveryPending;

  /// No description provided for @deliverySending.
  ///
  /// In en, this message translates to:
  /// **'Sending'**
  String get deliverySending;

  /// No description provided for @deliveryDelivered.
  ///
  /// In en, this message translates to:
  /// **'Delivered'**
  String get deliveryDelivered;

  /// No description provided for @deliveryFailed.
  ///
  /// In en, this message translates to:
  /// **'Failed'**
  String get deliveryFailed;

  /// No description provided for @deliverySuppressed.
  ///
  /// In en, this message translates to:
  /// **'Suppressed'**
  String get deliverySuppressed;

  /// No description provided for @deliveryUnknown.
  ///
  /// In en, this message translates to:
  /// **'Unknown state'**
  String get deliveryUnknown;

  /// No description provided for @deliveryAttempts.
  ///
  /// In en, this message translates to:
  /// **'{count} attempts'**
  String deliveryAttempts(int count);

  /// No description provided for @serviceTabOverview.
  ///
  /// In en, this message translates to:
  /// **'Overview'**
  String get serviceTabOverview;

  /// No description provided for @serviceTabErrors.
  ///
  /// In en, this message translates to:
  /// **'Errors'**
  String get serviceTabErrors;

  /// No description provided for @errorsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No errors with this filter.'**
  String get errorsEmpty;

  /// No description provided for @errorsOccurrences.
  ///
  /// In en, this message translates to:
  /// **'{count} times'**
  String errorsOccurrences(int count);

  /// No description provided for @errorsLastSeen.
  ///
  /// In en, this message translates to:
  /// **'last {when}'**
  String errorsLastSeen(String when);

  /// No description provided for @errorStatusUnresolved.
  ///
  /// In en, this message translates to:
  /// **'Unresolved'**
  String get errorStatusUnresolved;

  /// No description provided for @errorStatusResolved.
  ///
  /// In en, this message translates to:
  /// **'Resolved'**
  String get errorStatusResolved;

  /// No description provided for @errorStatusIgnored.
  ///
  /// In en, this message translates to:
  /// **'Ignored'**
  String get errorStatusIgnored;

  /// No description provided for @errorStatusUnknown.
  ///
  /// In en, this message translates to:
  /// **'Unknown state'**
  String get errorStatusUnknown;

  /// No description provided for @errorsNoTrace.
  ///
  /// In en, this message translates to:
  /// **'No trace was kept for this error.'**
  String get errorsNoTrace;

  /// No description provided for @errorsTruncated.
  ///
  /// In en, this message translates to:
  /// **'Not every matching group; narrow the filter.'**
  String get errorsTruncated;

  /// No description provided for @traceTitle.
  ///
  /// In en, this message translates to:
  /// **'Trace'**
  String get traceTitle;

  /// No description provided for @traceSpans.
  ///
  /// In en, this message translates to:
  /// **'{count} spans'**
  String traceSpans(int count);

  /// No description provided for @traceEmpty.
  ///
  /// In en, this message translates to:
  /// **'This trace has no spans.'**
  String get traceEmpty;

  /// No description provided for @traceRoot.
  ///
  /// In en, this message translates to:
  /// **'root'**
  String get traceRoot;

  /// No description provided for @navQuery.
  ///
  /// In en, this message translates to:
  /// **'Query'**
  String get navQuery;

  /// No description provided for @queryHint.
  ///
  /// In en, this message translates to:
  /// **'SELECT count(*) FROM logs SINCE 1 hour ago'**
  String get queryHint;

  /// No description provided for @queryRun.
  ///
  /// In en, this message translates to:
  /// **'Run'**
  String get queryRun;

  /// No description provided for @queryEmpty.
  ///
  /// In en, this message translates to:
  /// **'Write a query and press Run.'**
  String get queryEmpty;

  /// No description provided for @queryForbidden.
  ///
  /// In en, this message translates to:
  /// **'Your role does not allow running queries.'**
  String get queryForbidden;

  /// No description provided for @queryRejected.
  ///
  /// In en, this message translates to:
  /// **'The server would not run that: {detail}'**
  String queryRejected(String detail);

  /// No description provided for @queryMeta.
  ///
  /// In en, this message translates to:
  /// **'{rows} rows read in {ms} ms'**
  String queryMeta(int rows, int ms);

  /// No description provided for @queryRecent.
  ///
  /// In en, this message translates to:
  /// **'Recent'**
  String get queryRecent;

  /// No description provided for @queryTruncated.
  ///
  /// In en, this message translates to:
  /// **'The answer was cut short by the server\'s limit.'**
  String get queryTruncated;

  /// No description provided for @navTraces.
  ///
  /// In en, this message translates to:
  /// **'Traces'**
  String get navTraces;

  /// No description provided for @tracesEmpty.
  ///
  /// In en, this message translates to:
  /// **'No request has been traced in the window.'**
  String get tracesEmpty;

  /// No description provided for @tracesNewest.
  ///
  /// In en, this message translates to:
  /// **'Newest'**
  String get tracesNewest;

  /// No description provided for @tracesSlowest.
  ///
  /// In en, this message translates to:
  /// **'Slowest'**
  String get tracesSlowest;

  /// No description provided for @tracesError.
  ///
  /// In en, this message translates to:
  /// **'error'**
  String get tracesError;

  /// No description provided for @navMetrics.
  ///
  /// In en, this message translates to:
  /// **'Metrics'**
  String get navMetrics;

  /// No description provided for @metricsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No metric has reported in the window.'**
  String get metricsEmpty;

  /// No description provided for @metricSeriesCount.
  ///
  /// In en, this message translates to:
  /// **'{count} series'**
  String metricSeriesCount(int count);

  /// No description provided for @metricNoChart.
  ///
  /// In en, this message translates to:
  /// **'No series could be drawn: {detail}'**
  String metricNoChart(String detail);

  /// No description provided for @metricNoPoints.
  ///
  /// In en, this message translates to:
  /// **'This metric has no points in the window.'**
  String get metricNoPoints;

  /// No description provided for @metricAttributes.
  ///
  /// In en, this message translates to:
  /// **'Attributes'**
  String get metricAttributes;

  /// No description provided for @metricResourceKeys.
  ///
  /// In en, this message translates to:
  /// **'Resource keys'**
  String get metricResourceKeys;

  /// No description provided for @metricTruncated.
  ///
  /// In en, this message translates to:
  /// **'More series than the chart shows.'**
  String get metricTruncated;

  /// No description provided for @metricOneSeries.
  ///
  /// In en, this message translates to:
  /// **'One of {count} series.'**
  String metricOneSeries(int count);

  /// No description provided for @navRum.
  ///
  /// In en, this message translates to:
  /// **'Browser'**
  String get navRum;

  /// No description provided for @rumEmpty.
  ///
  /// In en, this message translates to:
  /// **'No browser application has reported in the window.'**
  String get rumEmpty;

  /// No description provided for @rumViews.
  ///
  /// In en, this message translates to:
  /// **'Page views'**
  String get rumViews;

  /// No description provided for @rumSessions.
  ///
  /// In en, this message translates to:
  /// **'Sessions'**
  String get rumSessions;

  /// No description provided for @rumErrors.
  ///
  /// In en, this message translates to:
  /// **'Errors'**
  String get rumErrors;

  /// No description provided for @rumAvgLoad.
  ///
  /// In en, this message translates to:
  /// **'Average load'**
  String get rumAvgLoad;

  /// No description provided for @rumVitals.
  ///
  /// In en, this message translates to:
  /// **'Core Web Vitals'**
  String get rumVitals;

  /// No description provided for @rumVitalGood.
  ///
  /// In en, this message translates to:
  /// **'good'**
  String get rumVitalGood;

  /// No description provided for @rumVitalNeedsImprovement.
  ///
  /// In en, this message translates to:
  /// **'needs work'**
  String get rumVitalNeedsImprovement;

  /// No description provided for @rumVitalPoor.
  ///
  /// In en, this message translates to:
  /// **'poor'**
  String get rumVitalPoor;

  /// No description provided for @rumVitalNoData.
  ///
  /// In en, this message translates to:
  /// **'no measurements'**
  String get rumVitalNoData;

  /// No description provided for @rumVitalShare.
  ///
  /// In en, this message translates to:
  /// **'{percent}% good'**
  String rumVitalShare(int percent);

  /// No description provided for @rumNoPoints.
  ///
  /// In en, this message translates to:
  /// **'No page view in the window.'**
  String get rumNoPoints;

  /// No description provided for @navCosts.
  ///
  /// In en, this message translates to:
  /// **'Costs'**
  String get navCosts;

  /// No description provided for @costsOff.
  ///
  /// In en, this message translates to:
  /// **'This installation does not estimate cost.'**
  String get costsOff;

  /// No description provided for @costsTotal.
  ///
  /// In en, this message translates to:
  /// **'Total'**
  String get costsTotal;

  /// No description provided for @costsPerHour.
  ///
  /// In en, this message translates to:
  /// **'Per hour'**
  String get costsPerHour;

  /// No description provided for @costsIdle.
  ///
  /// In en, this message translates to:
  /// **'Idle'**
  String get costsIdle;

  /// No description provided for @costsHosts.
  ///
  /// In en, this message translates to:
  /// **'Hosts'**
  String get costsHosts;

  /// No description provided for @costsEstimate.
  ///
  /// In en, this message translates to:
  /// **'An estimate from a price table, not a bill. Prices collected {updated}. {note}'**
  String costsEstimate(String updated, String note);

  /// No description provided for @costsUnpriced.
  ///
  /// In en, this message translates to:
  /// **'{count} hosts have no price.'**
  String costsUnpriced(int count);

  /// No description provided for @costsHostsTitle.
  ///
  /// In en, this message translates to:
  /// **'Most expensive hosts'**
  String get costsHostsTitle;

  /// No description provided for @costsUsed.
  ///
  /// In en, this message translates to:
  /// **'{percent}% used'**
  String costsUsed(int percent);

  /// No description provided for @costsNoPrice.
  ///
  /// In en, this message translates to:
  /// **'no price'**
  String get costsNoPrice;

  /// No description provided for @costsMoreHosts.
  ///
  /// In en, this message translates to:
  /// **'and {count} more'**
  String costsMoreHosts(int count);

  /// No description provided for @navInventory.
  ///
  /// In en, this message translates to:
  /// **'Inventory search'**
  String get navInventory;

  /// No description provided for @inventoryEmpty.
  ///
  /// In en, this message translates to:
  /// **'Nothing matches in this category.'**
  String get inventoryEmpty;

  /// No description provided for @inventoryHint.
  ///
  /// In en, this message translates to:
  /// **'Find items across all hosts — e.g. which hosts have openssl?'**
  String get inventoryHint;

  /// No description provided for @inventoryCategory.
  ///
  /// In en, this message translates to:
  /// **'Category'**
  String get inventoryCategory;

  /// No description provided for @inventorySearch.
  ///
  /// In en, this message translates to:
  /// **'Key contains'**
  String get inventorySearch;

  /// No description provided for @invOs.
  ///
  /// In en, this message translates to:
  /// **'OS'**
  String get invOs;

  /// No description provided for @invHardware.
  ///
  /// In en, this message translates to:
  /// **'Hardware'**
  String get invHardware;

  /// No description provided for @invPackage.
  ///
  /// In en, this message translates to:
  /// **'Packages'**
  String get invPackage;

  /// No description provided for @invProcess.
  ///
  /// In en, this message translates to:
  /// **'Processes'**
  String get invProcess;

  /// No description provided for @invListeningPort.
  ///
  /// In en, this message translates to:
  /// **'Listening ports'**
  String get invListeningPort;

  /// No description provided for @invSystemdUnit.
  ///
  /// In en, this message translates to:
  /// **'systemd units'**
  String get invSystemdUnit;

  /// No description provided for @invKernelModule.
  ///
  /// In en, this message translates to:
  /// **'Kernel modules'**
  String get invKernelModule;

  /// No description provided for @invNetworkInterface.
  ///
  /// In en, this message translates to:
  /// **'Network interfaces'**
  String get invNetworkInterface;

  /// No description provided for @invMount.
  ///
  /// In en, this message translates to:
  /// **'Mounts'**
  String get invMount;

  /// No description provided for @invUser.
  ///
  /// In en, this message translates to:
  /// **'Users'**
  String get invUser;

  /// No description provided for @invLaunchdService.
  ///
  /// In en, this message translates to:
  /// **'launchd services'**
  String get invLaunchdService;

  /// No description provided for @invWindowsService.
  ///
  /// In en, this message translates to:
  /// **'Windows services'**
  String get invWindowsService;

  /// No description provided for @invDiscoveredService.
  ///
  /// In en, this message translates to:
  /// **'Discovered services'**
  String get invDiscoveredService;

  /// No description provided for @navFleet.
  ///
  /// In en, this message translates to:
  /// **'Fleet'**
  String get navFleet;

  /// No description provided for @fleetEmpty.
  ///
  /// In en, this message translates to:
  /// **'No agent has reported.'**
  String get fleetEmpty;

  /// No description provided for @fleetAgents.
  ///
  /// In en, this message translates to:
  /// **'Agents'**
  String get fleetAgents;

  /// No description provided for @fleetOutdated.
  ///
  /// In en, this message translates to:
  /// **'Outdated'**
  String get fleetOutdated;

  /// No description provided for @fleetInProgress.
  ///
  /// In en, this message translates to:
  /// **'Updating'**
  String get fleetInProgress;

  /// No description provided for @fleetFailed.
  ///
  /// In en, this message translates to:
  /// **'Failed'**
  String get fleetFailed;

  /// No description provided for @fleetLatest.
  ///
  /// In en, this message translates to:
  /// **'Latest {version}'**
  String fleetLatest(String version);

  /// No description provided for @fleetNoCatalog.
  ///
  /// In en, this message translates to:
  /// **'No release catalogue; the server cannot tell what is latest.'**
  String get fleetNoCatalog;

  /// No description provided for @fleetReadOnly.
  ///
  /// In en, this message translates to:
  /// **'Read-only here. Rollouts and policy are changed on the web.'**
  String get fleetReadOnly;

  /// No description provided for @fleetUnsupported.
  ///
  /// In en, this message translates to:
  /// **'unsupported'**
  String get fleetUnsupported;

  /// No description provided for @fleetStatePrefix.
  ///
  /// In en, this message translates to:
  /// **'update {state}'**
  String fleetStatePrefix(String state);

  /// No description provided for @fleetStateIdle.
  ///
  /// In en, this message translates to:
  /// **'idle'**
  String get fleetStateIdle;

  /// No description provided for @fleetStateDownloading.
  ///
  /// In en, this message translates to:
  /// **'downloading'**
  String get fleetStateDownloading;

  /// No description provided for @fleetStateVerifying.
  ///
  /// In en, this message translates to:
  /// **'verifying'**
  String get fleetStateVerifying;

  /// No description provided for @fleetStateStaged.
  ///
  /// In en, this message translates to:
  /// **'staged'**
  String get fleetStateStaged;

  /// No description provided for @fleetStateRestarting.
  ///
  /// In en, this message translates to:
  /// **'restarting'**
  String get fleetStateRestarting;

  /// No description provided for @fleetStateConfirming.
  ///
  /// In en, this message translates to:
  /// **'confirming'**
  String get fleetStateConfirming;

  /// No description provided for @fleetStateSucceeded.
  ///
  /// In en, this message translates to:
  /// **'succeeded'**
  String get fleetStateSucceeded;

  /// No description provided for @fleetStateFailed.
  ///
  /// In en, this message translates to:
  /// **'failed'**
  String get fleetStateFailed;

  /// No description provided for @fleetStateRolledBack.
  ///
  /// In en, this message translates to:
  /// **'rolled back'**
  String get fleetStateRolledBack;

  /// No description provided for @navIntegrations.
  ///
  /// In en, this message translates to:
  /// **'Integrations'**
  String get navIntegrations;

  /// No description provided for @integrationsEmpty.
  ///
  /// In en, this message translates to:
  /// **'The agents have not discovered an integration.'**
  String get integrationsEmpty;

  /// No description provided for @integrationsSearch.
  ///
  /// In en, this message translates to:
  /// **'Search integrations'**
  String get integrationsSearch;

  /// No description provided for @integEnabled.
  ///
  /// In en, this message translates to:
  /// **'collecting'**
  String get integEnabled;

  /// No description provided for @integNeedsConfig.
  ///
  /// In en, this message translates to:
  /// **'needs configuration'**
  String get integNeedsConfig;

  /// No description provided for @integError.
  ///
  /// In en, this message translates to:
  /// **'error'**
  String get integError;

  /// No description provided for @integNotAvailable.
  ///
  /// In en, this message translates to:
  /// **'not available'**
  String get integNotAvailable;

  /// No description provided for @integCounts.
  ///
  /// In en, this message translates to:
  /// **'{enabled} collecting, {needs} need configuration, {error} failing'**
  String integCounts(int enabled, int needs, int error);

  /// No description provided for @integConfigureOnWeb.
  ///
  /// In en, this message translates to:
  /// **'Configure on the web; this screen reads what the agents report.'**
  String get integConfigureOnWeb;

  /// No description provided for @navProfiles.
  ///
  /// In en, this message translates to:
  /// **'Profiling'**
  String get navProfiles;

  /// No description provided for @profilesEmpty.
  ///
  /// In en, this message translates to:
  /// **'Nothing has been profiled in the window.'**
  String get profilesEmpty;

  /// No description provided for @profilesSearch.
  ///
  /// In en, this message translates to:
  /// **'Search services and types'**
  String get profilesSearch;

  /// No description provided for @profileSamples.
  ///
  /// In en, this message translates to:
  /// **'{count} samples'**
  String profileSamples(int count);

  /// No description provided for @profileFunctions.
  ///
  /// In en, this message translates to:
  /// **'By self time'**
  String get profileFunctions;

  /// No description provided for @profileNoFunctions.
  ///
  /// In en, this message translates to:
  /// **'No function carries any of this profile.'**
  String get profileNoFunctions;

  /// No description provided for @profileShare.
  ///
  /// In en, this message translates to:
  /// **'{percent}%'**
  String profileShare(String percent);

  /// No description provided for @profileOfShown.
  ///
  /// In en, this message translates to:
  /// **'Shares are of the rows shown, not of the whole window.'**
  String get profileOfShown;

  /// No description provided for @navAddData.
  ///
  /// In en, this message translates to:
  /// **'Add data'**
  String get navAddData;

  /// No description provided for @addDataOtlpHttp.
  ///
  /// In en, this message translates to:
  /// **'OTLP over HTTP'**
  String get addDataOtlpHttp;

  /// No description provided for @addDataOtlpGrpc.
  ///
  /// In en, this message translates to:
  /// **'OTLP over gRPC'**
  String get addDataOtlpGrpc;

  /// No description provided for @addDataCopied.
  ///
  /// In en, this message translates to:
  /// **'Copied'**
  String get addDataCopied;

  /// No description provided for @addDataVersions.
  ///
  /// In en, this message translates to:
  /// **'Versions'**
  String get addDataVersions;

  /// No description provided for @addDataServer.
  ///
  /// In en, this message translates to:
  /// **'Server {version}'**
  String addDataServer(String version);

  /// No description provided for @addDataAgent.
  ///
  /// In en, this message translates to:
  /// **'Agents pinned to {version} ({channel})'**
  String addDataAgent(String version, String channel);

  /// No description provided for @addDataAgentDev.
  ///
  /// In en, this message translates to:
  /// **'A development build; the commands pin nothing.'**
  String get addDataAgentDev;

  /// No description provided for @addDataBrowser.
  ///
  /// In en, this message translates to:
  /// **'Browser data'**
  String get addDataBrowser;

  /// No description provided for @addDataCorsOn.
  ///
  /// In en, this message translates to:
  /// **'Allowed from: {origins}'**
  String addDataCorsOn(String origins);

  /// No description provided for @addDataCorsOff.
  ///
  /// In en, this message translates to:
  /// **'Not configured, so a browser cannot send to this server.'**
  String get addDataCorsOff;

  /// No description provided for @addDataSourcesOnWeb.
  ///
  /// In en, this message translates to:
  /// **'The data sources and their install commands are on the web; this screen has what you need to point something at this server.'**
  String get addDataSourcesOnWeb;

  /// No description provided for @addDataEndpointDerived.
  ///
  /// In en, this message translates to:
  /// **'Derived, not configured — check it is reachable from outside.'**
  String get addDataEndpointDerived;

  /// No description provided for @onboardingForbidden.
  ///
  /// In en, this message translates to:
  /// **'Your role does not allow reading this.'**
  String get onboardingForbidden;

  /// No description provided for @hostRuns.
  ///
  /// In en, this message translates to:
  /// **'Running here'**
  String get hostRuns;

  /// No description provided for @hostNoServices.
  ///
  /// In en, this message translates to:
  /// **'The agent has not reported a snapshot for this host yet.'**
  String get hostNoServices;

  /// No description provided for @hostServicesFailed.
  ///
  /// In en, this message translates to:
  /// **'What runs here could not be read: {detail}'**
  String hostServicesFailed(String detail);

  /// No description provided for @hostNothingFound.
  ///
  /// In en, this message translates to:
  /// **'The agent found nothing it recognises.'**
  String get hostNothingFound;

  /// No description provided for @hostCpu.
  ///
  /// In en, this message translates to:
  /// **'CPU'**
  String get hostCpu;

  /// No description provided for @hostMemory.
  ///
  /// In en, this message translates to:
  /// **'Memory'**
  String get hostMemory;

  /// No description provided for @hostDisk.
  ///
  /// In en, this message translates to:
  /// **'Disk'**
  String get hostDisk;

  /// No description provided for @hostLoad.
  ///
  /// In en, this message translates to:
  /// **'Load'**
  String get hostLoad;

  /// No description provided for @hostAgent.
  ///
  /// In en, this message translates to:
  /// **'Agent {version}'**
  String hostAgent(String version);

  /// No description provided for @hostAttributes.
  ///
  /// In en, this message translates to:
  /// **'Resource attributes'**
  String get hostAttributes;

  /// No description provided for @containerCpu.
  ///
  /// In en, this message translates to:
  /// **'CPU'**
  String get containerCpu;

  /// No description provided for @containerMemory.
  ///
  /// In en, this message translates to:
  /// **'Memory'**
  String get containerMemory;

  /// No description provided for @containerNetwork.
  ///
  /// In en, this message translates to:
  /// **'Network'**
  String get containerNetwork;

  /// No description provided for @containerDisk.
  ///
  /// In en, this message translates to:
  /// **'Block I/O'**
  String get containerDisk;

  /// No description provided for @containerNoSeries.
  ///
  /// In en, this message translates to:
  /// **'Nothing was sampled in the window.'**
  String get containerNoSeries;

  /// No description provided for @containerSeriesFailed.
  ///
  /// In en, this message translates to:
  /// **'The charts could not be read: {detail}'**
  String containerSeriesFailed(String detail);

  /// No description provided for @containerNoLimit.
  ///
  /// In en, this message translates to:
  /// **'No memory limit, so there is no share to show.'**
  String get containerNoLimit;

  /// No description provided for @containerRestarts.
  ///
  /// In en, this message translates to:
  /// **'{count} restarts'**
  String containerRestarts(int count);

  /// No description provided for @containerImage.
  ///
  /// In en, this message translates to:
  /// **'Image'**
  String get containerImage;

  /// No description provided for @containerAttributes.
  ///
  /// In en, this message translates to:
  /// **'Attributes'**
  String get containerAttributes;

  /// No description provided for @containerOn.
  ///
  /// In en, this message translates to:
  /// **'on {host}'**
  String containerOn(String host);

  /// No description provided for @containerRxTx.
  ///
  /// In en, this message translates to:
  /// **'in / out'**
  String get containerRxTx;

  /// No description provided for @containerReadWrite.
  ///
  /// In en, this message translates to:
  /// **'read / write'**
  String get containerReadWrite;

  /// No description provided for @podContainers.
  ///
  /// In en, this message translates to:
  /// **'Containers'**
  String get podContainers;

  /// No description provided for @podEvents.
  ///
  /// In en, this message translates to:
  /// **'Events'**
  String get podEvents;

  /// No description provided for @podNoEvents.
  ///
  /// In en, this message translates to:
  /// **'No event about this pod.'**
  String get podNoEvents;

  /// No description provided for @podEventsFailed.
  ///
  /// In en, this message translates to:
  /// **'The events could not be read: {detail}'**
  String podEventsFailed(String detail);

  /// No description provided for @podLabels.
  ///
  /// In en, this message translates to:
  /// **'Labels'**
  String get podLabels;

  /// No description provided for @podServices.
  ///
  /// In en, this message translates to:
  /// **'Services'**
  String get podServices;

  /// No description provided for @podRestartsLabel.
  ///
  /// In en, this message translates to:
  /// **'Restarts'**
  String get podRestartsLabel;

  /// No description provided for @podNode.
  ///
  /// In en, this message translates to:
  /// **'Node'**
  String get podNode;

  /// No description provided for @podReady.
  ///
  /// In en, this message translates to:
  /// **'ready'**
  String get podReady;

  /// No description provided for @podNotReady.
  ///
  /// In en, this message translates to:
  /// **'not ready'**
  String get podNotReady;

  /// No description provided for @podEventCount.
  ///
  /// In en, this message translates to:
  /// **'{count}×'**
  String podEventCount(int count);

  /// No description provided for @podCpu.
  ///
  /// In en, this message translates to:
  /// **'CPU'**
  String get podCpu;

  /// No description provided for @podMemory.
  ///
  /// In en, this message translates to:
  /// **'Working set'**
  String get podMemory;

  /// No description provided for @navRules.
  ///
  /// In en, this message translates to:
  /// **'Rules'**
  String get navRules;

  /// No description provided for @rulesEmpty.
  ///
  /// In en, this message translates to:
  /// **'No alert rule.'**
  String get rulesEmpty;

  /// No description provided for @rulesSearch.
  ///
  /// In en, this message translates to:
  /// **'Search rules'**
  String get rulesSearch;

  /// No description provided for @ruleFiring.
  ///
  /// In en, this message translates to:
  /// **'firing'**
  String get ruleFiring;

  /// No description provided for @rulePending.
  ///
  /// In en, this message translates to:
  /// **'pending'**
  String get rulePending;

  /// No description provided for @ruleOk.
  ///
  /// In en, this message translates to:
  /// **'ok'**
  String get ruleOk;

  /// No description provided for @ruleError.
  ///
  /// In en, this message translates to:
  /// **'error'**
  String get ruleError;

  /// No description provided for @ruleDisabled.
  ///
  /// In en, this message translates to:
  /// **'off'**
  String get ruleDisabled;

  /// No description provided for @ruleOpenIncidents.
  ///
  /// In en, this message translates to:
  /// **'{count} open'**
  String ruleOpenIncidents(int count);

  /// No description provided for @ruleEvery.
  ///
  /// In en, this message translates to:
  /// **'every {seconds}s'**
  String ruleEvery(int seconds);

  /// No description provided for @ruleDisableTitle.
  ///
  /// In en, this message translates to:
  /// **'Turn this rule off?'**
  String get ruleDisableTitle;

  /// No description provided for @ruleDisableBody.
  ///
  /// In en, this message translates to:
  /// **'It will stop evaluating, and its {count} open incidents are resolved as well. Anything it is paging about now stops being tracked.'**
  String ruleDisableBody(int count);

  /// No description provided for @ruleDisableBodyNone.
  ///
  /// In en, this message translates to:
  /// **'It will stop evaluating until someone turns it back on.'**
  String get ruleDisableBodyNone;

  /// No description provided for @ruleDisableConfirm.
  ///
  /// In en, this message translates to:
  /// **'Turn off'**
  String get ruleDisableConfirm;

  /// No description provided for @ruleEnableTitle.
  ///
  /// In en, this message translates to:
  /// **'Turn this rule back on?'**
  String get ruleEnableTitle;

  /// No description provided for @ruleEnableConfirm.
  ///
  /// In en, this message translates to:
  /// **'Turn on'**
  String get ruleEnableConfirm;

  /// No description provided for @ruleCancel.
  ///
  /// In en, this message translates to:
  /// **'Cancel'**
  String get ruleCancel;

  /// No description provided for @ruleEditOnWeb.
  ///
  /// In en, this message translates to:
  /// **'Thresholds, conditions and channels are edited on the web.'**
  String get ruleEditOnWeb;

  /// No description provided for @navChannels.
  ///
  /// In en, this message translates to:
  /// **'Channels'**
  String get navChannels;

  /// No description provided for @channelsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No notification channel.'**
  String get channelsEmpty;

  /// No description provided for @channelsSearch.
  ///
  /// In en, this message translates to:
  /// **'Search channels'**
  String get channelsSearch;

  /// No description provided for @channelsNoSecrets.
  ///
  /// In en, this message translates to:
  /// **'This installation has no secrets key, so channels cannot be stored or tested.'**
  String get channelsNoSecrets;

  /// No description provided for @channelTest.
  ///
  /// In en, this message translates to:
  /// **'Send a test'**
  String get channelTest;

  /// No description provided for @channelTestOk.
  ///
  /// In en, this message translates to:
  /// **'Reached in {ms} ms'**
  String channelTestOk(int ms);

  /// No description provided for @channelTestFailedCode.
  ///
  /// In en, this message translates to:
  /// **'Refused with {code}: {error}'**
  String channelTestFailedCode(int code, String error);

  /// No description provided for @channelTestFailed.
  ///
  /// In en, this message translates to:
  /// **'Did not get through: {error}'**
  String channelTestFailed(String error);

  /// No description provided for @channelLastDelivery.
  ///
  /// In en, this message translates to:
  /// **'last {when}'**
  String channelLastDelivery(String when);

  /// No description provided for @channelOff.
  ///
  /// In en, this message translates to:
  /// **'off'**
  String get channelOff;

  /// No description provided for @channelEditOnWeb.
  ///
  /// In en, this message translates to:
  /// **'Channels are created and edited on the web; here you can check one still works.'**
  String get channelEditOnWeb;

  /// No description provided for @sessionsTitle.
  ///
  /// In en, this message translates to:
  /// **'Signed in on'**
  String get sessionsTitle;

  /// No description provided for @sessionsThisDevice.
  ///
  /// In en, this message translates to:
  /// **'this device'**
  String get sessionsThisDevice;

  /// No description provided for @sessionsBrowser.
  ///
  /// In en, this message translates to:
  /// **'browser'**
  String get sessionsBrowser;

  /// No description provided for @sessionsLastSeen.
  ///
  /// In en, this message translates to:
  /// **'last used {when}'**
  String sessionsLastSeen(String when);

  /// No description provided for @sessionsExpires.
  ///
  /// In en, this message translates to:
  /// **'expires {when}'**
  String sessionsExpires(String when);

  /// No description provided for @sessionsEnd.
  ///
  /// In en, this message translates to:
  /// **'End'**
  String get sessionsEnd;

  /// No description provided for @sessionsEndTitle.
  ///
  /// In en, this message translates to:
  /// **'End this session?'**
  String get sessionsEndTitle;

  /// No description provided for @sessionsEndBody.
  ///
  /// In en, this message translates to:
  /// **'Whatever is signed in there is signed out. If it is a device you no longer have, this is the thing to do.'**
  String get sessionsEndBody;

  /// No description provided for @sessionsNone.
  ///
  /// In en, this message translates to:
  /// **'No other session.'**
  String get sessionsNone;

  /// No description provided for @sessionsFailed.
  ///
  /// In en, this message translates to:
  /// **'The sessions could not be read: {detail}'**
  String sessionsFailed(String detail);

  /// No description provided for @logsService.
  ///
  /// In en, this message translates to:
  /// **'Service'**
  String get logsService;

  /// No description provided for @logsScopedTrace.
  ///
  /// In en, this message translates to:
  /// **'Logs of this request'**
  String get logsScopedTrace;

  /// No description provided for @logsScopedPod.
  ///
  /// In en, this message translates to:
  /// **'Logs of this pod'**
  String get logsScopedPod;

  /// No description provided for @logsScopedContainer.
  ///
  /// In en, this message translates to:
  /// **'Logs of this container'**
  String get logsScopedContainer;

  /// No description provided for @logsScopedAll.
  ///
  /// In en, this message translates to:
  /// **'Every level, because a request\'s logs are all of them.'**
  String get logsScopedAll;

  /// No description provided for @logsOpenForTrace.
  ///
  /// In en, this message translates to:
  /// **'Logs'**
  String get logsOpenForTrace;

  /// No description provided for @logsEmptyScoped.
  ///
  /// In en, this message translates to:
  /// **'Nothing was logged for this.'**
  String get logsEmptyScoped;

  /// No description provided for @navMutes.
  ///
  /// In en, this message translates to:
  /// **'Mutes'**
  String get navMutes;

  /// No description provided for @mutesEmpty.
  ///
  /// In en, this message translates to:
  /// **'Nothing is silenced.'**
  String get mutesEmpty;

  /// No description provided for @mutesSearch.
  ///
  /// In en, this message translates to:
  /// **'Search mutes'**
  String get mutesSearch;

  /// No description provided for @mutesActive.
  ///
  /// In en, this message translates to:
  /// **'silencing now'**
  String get mutesActive;

  /// No description provided for @mutesUpcoming.
  ///
  /// In en, this message translates to:
  /// **'starts {when}'**
  String mutesUpcoming(String when);

  /// No description provided for @mutesUntil.
  ///
  /// In en, this message translates to:
  /// **'until {when}'**
  String mutesUntil(String when);

  /// No description provided for @mutesEnd.
  ///
  /// In en, this message translates to:
  /// **'End now'**
  String get mutesEnd;

  /// No description provided for @mutesEndTitle.
  ///
  /// In en, this message translates to:
  /// **'End this mute?'**
  String get mutesEndTitle;

  /// No description provided for @mutesEndBody.
  ///
  /// In en, this message translates to:
  /// **'Alerting starts again immediately for whatever this was silencing.'**
  String get mutesEndBody;

  /// No description provided for @mutesNew.
  ///
  /// In en, this message translates to:
  /// **'Silence alerting'**
  String get mutesNew;

  /// No description provided for @mutesNewName.
  ///
  /// In en, this message translates to:
  /// **'Why'**
  String get mutesNewName;

  /// No description provided for @mutesNewNameHint.
  ///
  /// In en, this message translates to:
  /// **'e.g. deploying checkout'**
  String get mutesNewNameHint;

  /// No description provided for @mutesFor.
  ///
  /// In en, this message translates to:
  /// **'for'**
  String get mutesFor;

  /// No description provided for @mutesCreate.
  ///
  /// In en, this message translates to:
  /// **'Silence'**
  String get mutesCreate;

  /// No description provided for @mutesAllRules.
  ///
  /// In en, this message translates to:
  /// **'every rule'**
  String get mutesAllRules;

  /// No description provided for @mutesSomeRules.
  ///
  /// In en, this message translates to:
  /// **'{count} rules'**
  String mutesSomeRules(int count);

  /// No description provided for @mutesRecurring.
  ///
  /// In en, this message translates to:
  /// **'recurring; edited on the web'**
  String get mutesRecurring;

  /// No description provided for @mutesDuration30m.
  ///
  /// In en, this message translates to:
  /// **'30 min'**
  String get mutesDuration30m;

  /// No description provided for @mutesDuration1h.
  ///
  /// In en, this message translates to:
  /// **'1 hour'**
  String get mutesDuration1h;

  /// No description provided for @mutesDuration2h.
  ///
  /// In en, this message translates to:
  /// **'2 hours'**
  String get mutesDuration2h;

  /// No description provided for @mutesDuration4h.
  ///
  /// In en, this message translates to:
  /// **'4 hours'**
  String get mutesDuration4h;

  /// No description provided for @rightNow.
  ///
  /// In en, this message translates to:
  /// **'any moment'**
  String get rightNow;

  /// No description provided for @inMinutes.
  ///
  /// In en, this message translates to:
  /// **'in {count} min'**
  String inMinutes(int count);

  /// No description provided for @inHours.
  ///
  /// In en, this message translates to:
  /// **'in {count} h'**
  String inHours(int count);

  /// No description provided for @inDays.
  ///
  /// In en, this message translates to:
  /// **'in {count} days'**
  String inDays(int count);

  /// No description provided for @navRoutes.
  ///
  /// In en, this message translates to:
  /// **'Routing'**
  String get navRoutes;

  /// No description provided for @routesEmpty.
  ///
  /// In en, this message translates to:
  /// **'No routing rule; every incident goes to the rule\'s own channels.'**
  String get routesEmpty;

  /// No description provided for @routesOrder.
  ///
  /// In en, this message translates to:
  /// **'Tried in this order; the first match wins. Drag to reorder.'**
  String get routesOrder;

  /// No description provided for @routesDefault.
  ///
  /// In en, this message translates to:
  /// **'default'**
  String get routesDefault;

  /// No description provided for @routesOff.
  ///
  /// In en, this message translates to:
  /// **'off'**
  String get routesOff;

  /// No description provided for @routesChannels.
  ///
  /// In en, this message translates to:
  /// **'{count} channels'**
  String routesChannels(int count);

  /// No description provided for @routesMatchAll.
  ///
  /// In en, this message translates to:
  /// **'matches everything'**
  String get routesMatchAll;

  /// No description provided for @routesSeverities.
  ///
  /// In en, this message translates to:
  /// **'severity {list}'**
  String routesSeverities(String list);

  /// No description provided for @routesServices.
  ///
  /// In en, this message translates to:
  /// **'service {list}'**
  String routesServices(String list);

  /// No description provided for @routesTypes.
  ///
  /// In en, this message translates to:
  /// **'type {list}'**
  String routesTypes(String list);

  /// No description provided for @routesLabels.
  ///
  /// In en, this message translates to:
  /// **'{count} label conditions'**
  String routesLabels(int count);

  /// No description provided for @routesWindow.
  ///
  /// In en, this message translates to:
  /// **'{start}–{end} {tz}'**
  String routesWindow(String start, String end, String tz);

  /// No description provided for @routesEditOnWeb.
  ///
  /// In en, this message translates to:
  /// **'Conditions and channels are edited on the web.'**
  String get routesEditOnWeb;

  /// No description provided for @calendarsTitle.
  ///
  /// In en, this message translates to:
  /// **'Holiday calendars'**
  String get calendarsTitle;

  /// No description provided for @calendarsAbout.
  ///
  /// In en, this message translates to:
  /// **'Named lists of dates a recurring mute skips, such as public holidays.'**
  String get calendarsAbout;

  /// No description provided for @calendarsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No holiday calendars.'**
  String get calendarsEmpty;

  /// No description provided for @calendarsNew.
  ///
  /// In en, this message translates to:
  /// **'New holiday calendar'**
  String get calendarsNew;

  /// No description provided for @calendarsName.
  ///
  /// In en, this message translates to:
  /// **'Name'**
  String get calendarsName;

  /// No description provided for @calendarsDescription.
  ///
  /// In en, this message translates to:
  /// **'Description'**
  String get calendarsDescription;

  /// No description provided for @calendarsDates.
  ///
  /// In en, this message translates to:
  /// **'Dates'**
  String get calendarsDates;

  /// No description provided for @calendarsDatesHint.
  ///
  /// In en, this message translates to:
  /// **'One per line: YYYY-MM-DD for one date, MM-DD for every year.'**
  String get calendarsDatesHint;

  /// No description provided for @calendarsInvalid.
  ///
  /// In en, this message translates to:
  /// **'Invalid dates: {list}'**
  String calendarsInvalid(String list);

  /// No description provided for @calendarsCount.
  ///
  /// In en, this message translates to:
  /// **'{count} dates'**
  String calendarsCount(int count);

  /// No description provided for @calendarsSummary.
  ///
  /// In en, this message translates to:
  /// **'{count} dates, used by {mutes} mutes'**
  String calendarsSummary(int count, int mutes);

  /// No description provided for @calendarsCreate.
  ///
  /// In en, this message translates to:
  /// **'Create'**
  String get calendarsCreate;

  /// No description provided for @calendarsSave.
  ///
  /// In en, this message translates to:
  /// **'Save'**
  String get calendarsSave;

  /// No description provided for @calendarsEdit.
  ///
  /// In en, this message translates to:
  /// **'Edit'**
  String get calendarsEdit;

  /// No description provided for @calendarsDelete.
  ///
  /// In en, this message translates to:
  /// **'Delete'**
  String get calendarsDelete;

  /// No description provided for @calendarsDeleteTitle.
  ///
  /// In en, this message translates to:
  /// **'Delete {name}?'**
  String calendarsDeleteTitle(String name);

  /// No description provided for @calendarsDeleteBody.
  ///
  /// In en, this message translates to:
  /// **'The calendar goes; no mute uses it.'**
  String get calendarsDeleteBody;

  /// No description provided for @calendarsDeleteInUse.
  ///
  /// In en, this message translates to:
  /// **'{count} mutes use this calendar; the server refuses to delete it while they do.'**
  String calendarsDeleteInUse(int count);

  /// No description provided for @mutesCalendars.
  ///
  /// In en, this message translates to:
  /// **'Holiday calendars'**
  String get mutesCalendars;

  /// No description provided for @deliveriesTitle.
  ///
  /// In en, this message translates to:
  /// **'Delivery log'**
  String get deliveriesTitle;

  /// No description provided for @deliveriesFor.
  ///
  /// In en, this message translates to:
  /// **'{channel} deliveries'**
  String deliveriesFor(String channel);

  /// No description provided for @deliveriesEmpty.
  ///
  /// In en, this message translates to:
  /// **'No deliveries with this filter.'**
  String get deliveriesEmpty;

  /// No description provided for @deliveriesAll.
  ///
  /// In en, this message translates to:
  /// **'All'**
  String get deliveriesAll;

  /// No description provided for @deliveriesOpen.
  ///
  /// In en, this message translates to:
  /// **'Delivery log'**
  String get deliveriesOpen;

  /// No description provided for @deliveryForRule.
  ///
  /// In en, this message translates to:
  /// **'rule: {rule}'**
  String deliveryForRule(String rule);

  /// No description provided for @deliveryAttempt.
  ///
  /// In en, this message translates to:
  /// **'Attempt {attempt}: {status}, {ms} ms'**
  String deliveryAttempt(int attempt, String status, int ms);

  /// No description provided for @deliveryAttemptOk.
  ///
  /// In en, this message translates to:
  /// **'succeeded'**
  String get deliveryAttemptOk;

  /// No description provided for @deliveryAttemptFailed.
  ///
  /// In en, this message translates to:
  /// **'failed'**
  String get deliveryAttemptFailed;

  /// No description provided for @templatesTitle.
  ///
  /// In en, this message translates to:
  /// **'New rule'**
  String get templatesTitle;

  /// No description provided for @templatesAll.
  ///
  /// In en, this message translates to:
  /// **'All'**
  String get templatesAll;

  /// No description provided for @templatesHosts.
  ///
  /// In en, this message translates to:
  /// **'Hosts'**
  String get templatesHosts;

  /// No description provided for @templatesContainers.
  ///
  /// In en, this message translates to:
  /// **'Containers'**
  String get templatesContainers;

  /// No description provided for @templatesApm.
  ///
  /// In en, this message translates to:
  /// **'APM'**
  String get templatesApm;

  /// No description provided for @templatesIntegrations.
  ///
  /// In en, this message translates to:
  /// **'Integrations'**
  String get templatesIntegrations;

  /// No description provided for @templatesKubernetes.
  ///
  /// In en, this message translates to:
  /// **'Kubernetes'**
  String get templatesKubernetes;

  /// No description provided for @templatesEmpty.
  ///
  /// In en, this message translates to:
  /// **'No ready-made rules in this category.'**
  String get templatesEmpty;

  /// No description provided for @templatesSetUp.
  ///
  /// In en, this message translates to:
  /// **'Set up'**
  String get templatesSetUp;

  /// No description provided for @templatesPreview.
  ///
  /// In en, this message translates to:
  /// **'Preview'**
  String get templatesPreview;

  /// No description provided for @templatesCreate.
  ///
  /// In en, this message translates to:
  /// **'Create rule'**
  String get templatesCreate;

  /// No description provided for @templatesCreated.
  ///
  /// In en, this message translates to:
  /// **'{name} created.'**
  String templatesCreated(String name);

  /// No description provided for @templatesWouldFire.
  ///
  /// In en, this message translates to:
  /// **'Would have fired {count} times in the last 6 hours.'**
  String templatesWouldFire(int count);

  /// No description provided for @templatesNoData.
  ///
  /// In en, this message translates to:
  /// **'No data for this rule to look at in the last 6 hours.'**
  String get templatesNoData;

  /// No description provided for @templatesThreshold.
  ///
  /// In en, this message translates to:
  /// **'Threshold: {value}'**
  String templatesThreshold(String value);

  /// No description provided for @templatesOneSeries.
  ///
  /// In en, this message translates to:
  /// **'The busiest of {count} series is drawn.'**
  String templatesOneSeries(int count);

  /// No description provided for @templatesReference.
  ///
  /// In en, this message translates to:
  /// **'{metric} is {value} now; the threshold is a ratio of it.'**
  String templatesReference(String metric, String value);

  /// No description provided for @templatesRequired.
  ///
  /// In en, this message translates to:
  /// **'Required.'**
  String get templatesRequired;

  /// No description provided for @templatesNumber.
  ///
  /// In en, this message translates to:
  /// **'Enter a number.'**
  String get templatesNumber;

  /// No description provided for @templatesRange.
  ///
  /// In en, this message translates to:
  /// **'Must be between {min} and {max}.'**
  String templatesRange(String min, String max);

  /// No description provided for @templatesSeconds.
  ///
  /// In en, this message translates to:
  /// **'s'**
  String get templatesSeconds;

  /// No description provided for @templatesPerSecond.
  ///
  /// In en, this message translates to:
  /// **'/s'**
  String get templatesPerSecond;

  /// No description provided for @rulesNew.
  ///
  /// In en, this message translates to:
  /// **'New rule'**
  String get rulesNew;

  /// No description provided for @templatesUnavailable.
  ///
  /// In en, this message translates to:
  /// **'Rule types this installation cannot use: {list}'**
  String templatesUnavailable(String list);

  /// No description provided for @templatesPercent.
  ///
  /// In en, this message translates to:
  /// **'{value}%'**
  String templatesPercent(String value);

  /// No description provided for @navErrors.
  ///
  /// In en, this message translates to:
  /// **'Errors'**
  String get navErrors;

  /// No description provided for @errorsSearch.
  ///
  /// In en, this message translates to:
  /// **'Search errors'**
  String get errorsSearch;

  /// No description provided for @errorsSort.
  ///
  /// In en, this message translates to:
  /// **'Sort'**
  String get errorsSort;

  /// No description provided for @errorsSortCount.
  ///
  /// In en, this message translates to:
  /// **'By count'**
  String get errorsSortCount;

  /// No description provided for @errorsSortLastSeen.
  ///
  /// In en, this message translates to:
  /// **'By last seen'**
  String get errorsSortLastSeen;

  /// No description provided for @errorsSortFirstSeen.
  ///
  /// In en, this message translates to:
  /// **'By first seen'**
  String get errorsSortFirstSeen;

  /// No description provided for @errorsAll.
  ///
  /// In en, this message translates to:
  /// **'All'**
  String get errorsAll;

  /// No description provided for @errorsUnresolved.
  ///
  /// In en, this message translates to:
  /// **'Unresolved'**
  String get errorsUnresolved;

  /// No description provided for @errorsResolved.
  ///
  /// In en, this message translates to:
  /// **'Resolved'**
  String get errorsResolved;

  /// No description provided for @errorsIgnored.
  ///
  /// In en, this message translates to:
  /// **'Ignored'**
  String get errorsIgnored;

  /// No description provided for @errorsRegressed.
  ///
  /// In en, this message translates to:
  /// **'Regressed'**
  String get errorsRegressed;

  /// No description provided for @errorsNoWorkflow.
  ///
  /// In en, this message translates to:
  /// **'This installation has no error workflow (it needs PostgreSQL); every group reads as unresolved.'**
  String get errorsNoWorkflow;

  /// No description provided for @errorsCount.
  ///
  /// In en, this message translates to:
  /// **'{count} times'**
  String errorsCount(int count);

  /// No description provided for @errorsCountOfTotal.
  ///
  /// In en, this message translates to:
  /// **'{count} in this range, {total} within retention'**
  String errorsCountOfTotal(int count, int total);

  /// No description provided for @errorsFirstSeen.
  ///
  /// In en, this message translates to:
  /// **'first {when}'**
  String errorsFirstSeen(String when);

  /// No description provided for @errorsResolvedIn.
  ///
  /// In en, this message translates to:
  /// **'resolved in {version}'**
  String errorsResolvedIn(String version);

  /// No description provided for @errorsRegressions.
  ///
  /// In en, this message translates to:
  /// **'came back {count} times'**
  String errorsRegressions(int count);

  /// No description provided for @errorsLastTrace.
  ///
  /// In en, this message translates to:
  /// **'Open the last trace'**
  String get errorsLastTrace;

  /// No description provided for @errorsResolve.
  ///
  /// In en, this message translates to:
  /// **'Resolve'**
  String get errorsResolve;

  /// No description provided for @errorsResolveInVersion.
  ///
  /// In en, this message translates to:
  /// **'Resolve in version…'**
  String get errorsResolveInVersion;

  /// No description provided for @errorsVersion.
  ///
  /// In en, this message translates to:
  /// **'Version'**
  String get errorsVersion;

  /// No description provided for @errorsVersionHint.
  ///
  /// In en, this message translates to:
  /// **'Seen again after this version and the group reopens by itself.'**
  String get errorsVersionHint;

  /// No description provided for @errorsIgnore.
  ///
  /// In en, this message translates to:
  /// **'Ignore'**
  String get errorsIgnore;

  /// No description provided for @errorsReopen.
  ///
  /// In en, this message translates to:
  /// **'Reopen'**
  String get errorsReopen;

  /// No description provided for @errorsComments.
  ///
  /// In en, this message translates to:
  /// **'Comments'**
  String get errorsComments;

  /// No description provided for @errorsNoComments.
  ///
  /// In en, this message translates to:
  /// **'No comments.'**
  String get errorsNoComments;

  /// No description provided for @errorsComment.
  ///
  /// In en, this message translates to:
  /// **'Comment'**
  String get errorsComment;

  /// No description provided for @errorsCommentTooLong.
  ///
  /// In en, this message translates to:
  /// **'The comment is too long.'**
  String get errorsCommentTooLong;

  /// No description provided for @errorsSend.
  ///
  /// In en, this message translates to:
  /// **'Send'**
  String get errorsSend;

  /// No description provided for @errorsDeleteComment.
  ///
  /// In en, this message translates to:
  /// **'Delete the comment'**
  String get errorsDeleteComment;

  /// No description provided for @agentsTitle.
  ///
  /// In en, this message translates to:
  /// **'Agent versions'**
  String get agentsTitle;

  /// No description provided for @agentsSearch.
  ///
  /// In en, this message translates to:
  /// **'Search service, agent or version'**
  String get agentsSearch;

  /// No description provided for @agentsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No agents with this filter.'**
  String get agentsEmpty;

  /// No description provided for @agentsOutdatedOnly.
  ///
  /// In en, this message translates to:
  /// **'Behind only'**
  String get agentsOutdatedOnly;

  /// No description provided for @agentsLatest.
  ///
  /// In en, this message translates to:
  /// **'Latest release {version} ({channel})'**
  String agentsLatest(String version, String channel);

  /// No description provided for @agentsNoCatalog.
  ///
  /// In en, this message translates to:
  /// **'The release catalog could not be read; statuses are unknown.'**
  String get agentsNoCatalog;

  /// No description provided for @agentsOk.
  ///
  /// In en, this message translates to:
  /// **'current'**
  String get agentsOk;

  /// No description provided for @agentsOutdated.
  ///
  /// In en, this message translates to:
  /// **'outdated'**
  String get agentsOutdated;

  /// No description provided for @agentsUnsupported.
  ///
  /// In en, this message translates to:
  /// **'unsupported'**
  String get agentsUnsupported;

  /// No description provided for @agentsThirdParty.
  ///
  /// In en, this message translates to:
  /// **'third party'**
  String get agentsThirdParty;

  /// No description provided for @agentsUnknown.
  ///
  /// In en, this message translates to:
  /// **'unknown'**
  String get agentsUnknown;

  /// No description provided for @agentsInstances.
  ///
  /// In en, this message translates to:
  /// **'{count} instances'**
  String agentsInstances(int count);

  /// No description provided for @servicesAgents.
  ///
  /// In en, this message translates to:
  /// **'Agent versions'**
  String get servicesAgents;

  /// No description provided for @tracesFilters.
  ///
  /// In en, this message translates to:
  /// **'Filters'**
  String get tracesFilters;

  /// No description provided for @tracesTransaction.
  ///
  /// In en, this message translates to:
  /// **'Transaction'**
  String get tracesTransaction;

  /// No description provided for @tracesMinMs.
  ///
  /// In en, this message translates to:
  /// **'Min ms'**
  String get tracesMinMs;

  /// No description provided for @tracesMaxMs.
  ///
  /// In en, this message translates to:
  /// **'Max ms'**
  String get tracesMaxMs;

  /// No description provided for @tracesAttributes.
  ///
  /// In en, this message translates to:
  /// **'Attributes'**
  String get tracesAttributes;

  /// No description provided for @tracesAttributesHint.
  ///
  /// In en, this message translates to:
  /// **'key=value, separated by spaces; at most 10.'**
  String get tracesAttributesHint;

  /// No description provided for @tracesErrorsOnly.
  ///
  /// In en, this message translates to:
  /// **'Errors only'**
  String get tracesErrorsOnly;

  /// No description provided for @tracesSearch.
  ///
  /// In en, this message translates to:
  /// **'Search'**
  String get tracesSearch;

  /// No description provided for @serviceTabTraces.
  ///
  /// In en, this message translates to:
  /// **'Traces'**
  String get serviceTabTraces;

  /// No description provided for @samplingTitle.
  ///
  /// In en, this message translates to:
  /// **'Sampling'**
  String get samplingTitle;

  /// No description provided for @samplingOffHere.
  ///
  /// In en, this message translates to:
  /// **'Tail sampling is off on this server; the policy is stored but not applied.'**
  String get samplingOffHere;

  /// No description provided for @samplingDefault.
  ///
  /// In en, this message translates to:
  /// **'No policy is stored; the server\'s default is shown.'**
  String get samplingDefault;

  /// No description provided for @samplingUpdated.
  ///
  /// In en, this message translates to:
  /// **'updated {when} by {email}'**
  String samplingUpdated(String when, String email);

  /// No description provided for @samplingEnabled.
  ///
  /// In en, this message translates to:
  /// **'Policy active'**
  String get samplingEnabled;

  /// No description provided for @samplingEnabledHint.
  ///
  /// In en, this message translates to:
  /// **'Off keeps every trace; the policy stays but drops nothing.'**
  String get samplingEnabledHint;

  /// No description provided for @samplingBaseline.
  ///
  /// In en, this message translates to:
  /// **'Baseline ratio'**
  String get samplingBaseline;

  /// No description provided for @samplingBaselineHint.
  ///
  /// In en, this message translates to:
  /// **'The share kept of traces that match no rule.'**
  String get samplingBaselineHint;

  /// No description provided for @samplingMaxSpans.
  ///
  /// In en, this message translates to:
  /// **'Max spans per second'**
  String get samplingMaxSpans;

  /// No description provided for @samplingMaxSpansHint.
  ///
  /// In en, this message translates to:
  /// **'Per sampler instance; 0 means unlimited.'**
  String get samplingMaxSpansHint;

  /// No description provided for @samplingPercentRange.
  ///
  /// In en, this message translates to:
  /// **'Enter a percentage between 0 and 100.'**
  String get samplingPercentRange;

  /// No description provided for @samplingRules.
  ///
  /// In en, this message translates to:
  /// **'Rules'**
  String get samplingRules;

  /// No description provided for @samplingOrder.
  ///
  /// In en, this message translates to:
  /// **'Tried in order; the first matching rule decides the ratio. Drag to reorder.'**
  String get samplingOrder;

  /// No description provided for @samplingNoRules.
  ///
  /// In en, this message translates to:
  /// **'No rules; every trace falls to the baseline.'**
  String get samplingNoRules;

  /// No description provided for @samplingAddRule.
  ///
  /// In en, this message translates to:
  /// **'Add rule'**
  String get samplingAddRule;

  /// No description provided for @samplingEditRule.
  ///
  /// In en, this message translates to:
  /// **'Edit rule'**
  String get samplingEditRule;

  /// No description provided for @samplingRuleName.
  ///
  /// In en, this message translates to:
  /// **'Name'**
  String get samplingRuleName;

  /// No description provided for @samplingRuleType.
  ///
  /// In en, this message translates to:
  /// **'Type'**
  String get samplingRuleType;

  /// No description provided for @samplingRuleRatio.
  ///
  /// In en, this message translates to:
  /// **'Keep ratio'**
  String get samplingRuleRatio;

  /// No description provided for @samplingRuleThreshold.
  ///
  /// In en, this message translates to:
  /// **'Threshold (ms)'**
  String get samplingRuleThreshold;

  /// No description provided for @samplingRuleServices.
  ///
  /// In en, this message translates to:
  /// **'Services'**
  String get samplingRuleServices;

  /// No description provided for @samplingRuleServicesHint.
  ///
  /// In en, this message translates to:
  /// **'Comma separated.'**
  String get samplingRuleServicesHint;

  /// No description provided for @samplingRuleService.
  ///
  /// In en, this message translates to:
  /// **'Service (optional)'**
  String get samplingRuleService;

  /// No description provided for @samplingRuleServiceHint.
  ///
  /// In en, this message translates to:
  /// **'Filled in, the rule looks only at this service\'s spans.'**
  String get samplingRuleServiceHint;

  /// No description provided for @samplingRuleRoute.
  ///
  /// In en, this message translates to:
  /// **'Route'**
  String get samplingRuleRoute;

  /// No description provided for @samplingRuleRouteHint.
  ///
  /// In en, this message translates to:
  /// **'Glob on http.route; a trailing * is a prefix match.'**
  String get samplingRuleRouteHint;

  /// No description provided for @samplingRuleKey.
  ///
  /// In en, this message translates to:
  /// **'Attribute key'**
  String get samplingRuleKey;

  /// No description provided for @samplingRuleValue.
  ///
  /// In en, this message translates to:
  /// **'Value (optional)'**
  String get samplingRuleValue;

  /// No description provided for @samplingRuleValueHint.
  ///
  /// In en, this message translates to:
  /// **'Left empty, the key only has to be present.'**
  String get samplingRuleValueHint;

  /// No description provided for @samplingTypeError.
  ///
  /// In en, this message translates to:
  /// **'failed traces'**
  String get samplingTypeError;

  /// No description provided for @samplingTypeLatency.
  ///
  /// In en, this message translates to:
  /// **'slow traces'**
  String get samplingTypeLatency;

  /// No description provided for @samplingTypeService.
  ///
  /// In en, this message translates to:
  /// **'by service'**
  String get samplingTypeService;

  /// No description provided for @samplingTypeRoute.
  ///
  /// In en, this message translates to:
  /// **'by route'**
  String get samplingTypeRoute;

  /// No description provided for @samplingTypeAttribute.
  ///
  /// In en, this message translates to:
  /// **'by attribute'**
  String get samplingTypeAttribute;

  /// No description provided for @samplingOverMs.
  ///
  /// In en, this message translates to:
  /// **'over {ms} ms'**
  String samplingOverMs(int ms);

  /// No description provided for @samplingKeepRatio.
  ///
  /// In en, this message translates to:
  /// **'keeps {percent}%'**
  String samplingKeepRatio(String percent);

  /// No description provided for @samplingMatched.
  ///
  /// In en, this message translates to:
  /// **'matched {percent}% of traces in the last estimate'**
  String samplingMatched(String percent);

  /// No description provided for @samplingEstimate.
  ///
  /// In en, this message translates to:
  /// **'Estimate'**
  String get samplingEstimate;

  /// No description provided for @samplingSave.
  ///
  /// In en, this message translates to:
  /// **'Save'**
  String get samplingSave;

  /// No description provided for @samplingKeeps.
  ///
  /// In en, this message translates to:
  /// **'{traces}% of traces and {spans}% of spans would be kept.'**
  String samplingKeeps(String traces, String spans);

  /// No description provided for @samplingExamined.
  ///
  /// In en, this message translates to:
  /// **'{count} traces in the last {minutes} minutes were examined.'**
  String samplingExamined(int count, int minutes);

  /// No description provided for @samplingNoRateLimit.
  ///
  /// In en, this message translates to:
  /// **'The rate limit is not part of this estimate.'**
  String get samplingNoRateLimit;

  /// No description provided for @samplingConflict.
  ///
  /// In en, this message translates to:
  /// **'Somebody else saved while you were editing; reload and try again.'**
  String get samplingConflict;

  /// No description provided for @samplingUnavailable.
  ///
  /// In en, this message translates to:
  /// **'This installation cannot store a sampling policy.'**
  String get samplingUnavailable;

  /// No description provided for @serviceTabMap.
  ///
  /// In en, this message translates to:
  /// **'Map'**
  String get serviceTabMap;

  /// No description provided for @mapEmpty.
  ///
  /// In en, this message translates to:
  /// **'No calls into or out of this service in this range.'**
  String get mapEmpty;

  /// No description provided for @mapIncoming.
  ///
  /// In en, this message translates to:
  /// **'Callers'**
  String get mapIncoming;

  /// No description provided for @mapIncomingHint.
  ///
  /// In en, this message translates to:
  /// **'Services that send requests to this one.'**
  String get mapIncomingHint;

  /// No description provided for @mapOutgoing.
  ///
  /// In en, this message translates to:
  /// **'Dependencies'**
  String get mapOutgoing;

  /// No description provided for @mapOutgoingHint.
  ///
  /// In en, this message translates to:
  /// **'Services, databases and external addresses this one depends on.'**
  String get mapOutgoingHint;

  /// No description provided for @mapKindDb.
  ///
  /// In en, this message translates to:
  /// **'database'**
  String get mapKindDb;

  /// No description provided for @mapKindExternal.
  ///
  /// In en, this message translates to:
  /// **'external'**
  String get mapKindExternal;

  /// No description provided for @mapKindMessaging.
  ///
  /// In en, this message translates to:
  /// **'queue'**
  String get mapKindMessaging;

  /// No description provided for @mapCalls.
  ///
  /// In en, this message translates to:
  /// **'{count} calls'**
  String mapCalls(int count);

  /// No description provided for @mapErrorRate.
  ///
  /// In en, this message translates to:
  /// **'{percent}% errors'**
  String mapErrorRate(String percent);

  /// No description provided for @mapP95.
  ///
  /// In en, this message translates to:
  /// **'p95 {ms} ms'**
  String mapP95(String ms);

  /// No description provided for @mapAvg.
  ///
  /// In en, this message translates to:
  /// **'avg {ms} ms'**
  String mapAvg(String ms);

  /// No description provided for @mapOnPath.
  ///
  /// In en, this message translates to:
  /// **'on this path'**
  String get mapOnPath;

  /// No description provided for @mapPathOf.
  ///
  /// In en, this message translates to:
  /// **'The dependencies {count} traces of {transaction} go through are marked.'**
  String mapPathOf(String transaction, int count);

  /// No description provided for @mapPathClear.
  ///
  /// In en, this message translates to:
  /// **'Clear the transaction'**
  String get mapPathClear;

  /// No description provided for @mapShowPath.
  ///
  /// In en, this message translates to:
  /// **'Show this transaction\'s path'**
  String get mapShowPath;

  /// No description provided for @navTemplates.
  ///
  /// In en, this message translates to:
  /// **'Templates'**
  String get navTemplates;

  /// No description provided for @navIncidents.
  ///
  /// In en, this message translates to:
  /// **'Incidents'**
  String get navIncidents;

  /// No description provided for @serviceTabTransactions.
  ///
  /// In en, this message translates to:
  /// **'Transactions'**
  String get serviceTabTransactions;

  /// No description provided for @serviceTabDatabases.
  ///
  /// In en, this message translates to:
  /// **'Databases'**
  String get serviceTabDatabases;

  /// No description provided for @transactionsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No transactions in this range.'**
  String get transactionsEmpty;

  /// No description provided for @serviceDatabasesEmpty.
  ///
  /// In en, this message translates to:
  /// **'No database queries in this range.'**
  String get serviceDatabasesEmpty;

  /// No description provided for @transactionsShare.
  ///
  /// In en, this message translates to:
  /// **'{percent}% of the time'**
  String transactionsShare(String percent);

  /// No description provided for @sortTimeConsumed.
  ///
  /// In en, this message translates to:
  /// **'Time consumed'**
  String get sortTimeConsumed;

  /// No description provided for @sortThroughput.
  ///
  /// In en, this message translates to:
  /// **'Throughput'**
  String get sortThroughput;

  /// No description provided for @sortCalls.
  ///
  /// In en, this message translates to:
  /// **'Calls'**
  String get sortCalls;

  /// No description provided for @sortSlowest.
  ///
  /// In en, this message translates to:
  /// **'Slowest'**
  String get sortSlowest;

  /// No description provided for @sortErrors.
  ///
  /// In en, this message translates to:
  /// **'Errors'**
  String get sortErrors;

  /// No description provided for @deploymentsTitle.
  ///
  /// In en, this message translates to:
  /// **'Deployments'**
  String get deploymentsTitle;

  /// No description provided for @deploymentsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No version changes in this range.'**
  String get deploymentsEmpty;

  /// No description provided for @deploymentsRollback.
  ///
  /// In en, this message translates to:
  /// **'rollback'**
  String get deploymentsRollback;

  /// No description provided for @deploymentsFirst.
  ///
  /// In en, this message translates to:
  /// **'first version'**
  String get deploymentsFirst;

  /// No description provided for @deploymentsWindow.
  ///
  /// In en, this message translates to:
  /// **'{minutes} minutes before and after'**
  String deploymentsWindow(int minutes);

  /// No description provided for @deploymentsNewErrors.
  ///
  /// In en, this message translates to:
  /// **'{count} error groups first seen after this deployment'**
  String deploymentsNewErrors(int count);

  /// No description provided for @serviceLatencyChart.
  ///
  /// In en, this message translates to:
  /// **'Latency (p95)'**
  String get serviceLatencyChart;

  /// No description provided for @serviceApdexChart.
  ///
  /// In en, this message translates to:
  /// **'Apdex'**
  String get serviceApdexChart;

  /// No description provided for @serviceTopTransactions.
  ///
  /// In en, this message translates to:
  /// **'Top transactions by time consumed'**
  String get serviceTopTransactions;

  /// No description provided for @serviceAllTransactions.
  ///
  /// In en, this message translates to:
  /// **'All transactions'**
  String get serviceAllTransactions;

  /// No description provided for @serviceHosts.
  ///
  /// In en, this message translates to:
  /// **'Hosts'**
  String get serviceHosts;

  /// No description provided for @serviceContainers.
  ///
  /// In en, this message translates to:
  /// **'Containers'**
  String get serviceContainers;

  /// No description provided for @servicePods.
  ///
  /// In en, this message translates to:
  /// **'Pods'**
  String get servicePods;

  /// No description provided for @apdexTitle.
  ///
  /// In en, this message translates to:
  /// **'Apdex threshold'**
  String get apdexTitle;

  /// No description provided for @apdexExplain.
  ///
  /// In en, this message translates to:
  /// **'Requests up to this duration count as satisfied; up to four times it, half satisfied.'**
  String get apdexExplain;

  /// No description provided for @apdexThreshold.
  ///
  /// In en, this message translates to:
  /// **'Milliseconds'**
  String get apdexThreshold;

  /// No description provided for @apdexDefault.
  ///
  /// In en, this message translates to:
  /// **'Apdex {ms} ms (default)'**
  String apdexDefault(int ms);

  /// No description provided for @apdexSet.
  ///
  /// In en, this message translates to:
  /// **'Apdex {ms} ms'**
  String apdexSet(int ms);

  /// No description provided for @apdexUnavailable.
  ///
  /// In en, this message translates to:
  /// **'This installation cannot store service settings.'**
  String get apdexUnavailable;

  /// No description provided for @serviceUnknownHost.
  ///
  /// In en, this message translates to:
  /// **'No agent data for this one; there is no page to open.'**
  String get serviceUnknownHost;

  /// No description provided for @errorsLastMessage.
  ///
  /// In en, this message translates to:
  /// **'Last message'**
  String get errorsLastMessage;

  /// No description provided for @errorsStacktrace.
  ///
  /// In en, this message translates to:
  /// **'Stack trace'**
  String get errorsStacktrace;

  /// No description provided for @errorsNoStack.
  ///
  /// In en, this message translates to:
  /// **'No stack trace for this group.'**
  String get errorsNoStack;

  /// No description provided for @errorsSymbolicated.
  ///
  /// In en, this message translates to:
  /// **'{count} frames resolved with a source map.'**
  String errorsSymbolicated(int count);

  /// No description provided for @errorsAffected.
  ///
  /// In en, this message translates to:
  /// **'Affected'**
  String get errorsAffected;

  /// No description provided for @errorsAffectedVersions.
  ///
  /// In en, this message translates to:
  /// **'Versions'**
  String get errorsAffectedVersions;

  /// No description provided for @errorsAffectedHosts.
  ///
  /// In en, this message translates to:
  /// **'Hosts'**
  String get errorsAffectedHosts;

  /// No description provided for @errorsAffectedContainers.
  ///
  /// In en, this message translates to:
  /// **'Containers'**
  String get errorsAffectedContainers;

  /// No description provided for @errorsAffectedTransactions.
  ///
  /// In en, this message translates to:
  /// **'Transactions'**
  String get errorsAffectedTransactions;

  /// No description provided for @errorsSamples.
  ///
  /// In en, this message translates to:
  /// **'Sample requests'**
  String get errorsSamples;

  /// No description provided for @errorsNoSamples.
  ///
  /// In en, this message translates to:
  /// **'No sample requests were kept.'**
  String get errorsNoSamples;

  /// No description provided for @errorsActivity.
  ///
  /// In en, this message translates to:
  /// **'History'**
  String get errorsActivity;

  /// No description provided for @hostApmServices.
  ///
  /// In en, this message translates to:
  /// **'Services sending traces'**
  String get hostApmServices;

  /// No description provided for @settingsProfile.
  ///
  /// In en, this message translates to:
  /// **'Profile'**
  String get settingsProfile;

  /// No description provided for @settingsSecurity.
  ///
  /// In en, this message translates to:
  /// **'Security'**
  String get settingsSecurity;

  /// No description provided for @profileLanguage.
  ///
  /// In en, this message translates to:
  /// **'Language'**
  String get profileLanguage;

  /// No description provided for @profileLanguageHint.
  ///
  /// In en, this message translates to:
  /// **'The language the server writes in: alert e-mails and generated rule names. The app\'s own language follows the phone.'**
  String get profileLanguageHint;

  /// No description provided for @profileLanguageAuto.
  ///
  /// In en, this message translates to:
  /// **'Automatic'**
  String get profileLanguageAuto;

  /// No description provided for @securityPassword.
  ///
  /// In en, this message translates to:
  /// **'Password'**
  String get securityPassword;

  /// No description provided for @securityPasswordHint.
  ///
  /// In en, this message translates to:
  /// **'Changing the password ends your other sessions; this device stays signed in.'**
  String get securityPasswordHint;

  /// No description provided for @securityCurrentPassword.
  ///
  /// In en, this message translates to:
  /// **'Current password'**
  String get securityCurrentPassword;

  /// No description provided for @securityNewPassword.
  ///
  /// In en, this message translates to:
  /// **'New password'**
  String get securityNewPassword;

  /// No description provided for @securityMinLength.
  ///
  /// In en, this message translates to:
  /// **'At least {count} characters.'**
  String securityMinLength(int count);

  /// No description provided for @securityChangePassword.
  ///
  /// In en, this message translates to:
  /// **'Change password'**
  String get securityChangePassword;

  /// No description provided for @securityPasswordChanged.
  ///
  /// In en, this message translates to:
  /// **'The password changed; the other sessions were ended.'**
  String get securityPasswordChanged;

  /// No description provided for @settingsMembers.
  ///
  /// In en, this message translates to:
  /// **'Members'**
  String get settingsMembers;

  /// No description provided for @membersTitle.
  ///
  /// In en, this message translates to:
  /// **'Members'**
  String get membersTitle;

  /// No description provided for @membersRole.
  ///
  /// In en, this message translates to:
  /// **'Role'**
  String get membersRole;

  /// No description provided for @membersYou.
  ///
  /// In en, this message translates to:
  /// **'you'**
  String get membersYou;

  /// No description provided for @membersJoined.
  ///
  /// In en, this message translates to:
  /// **'joined {when}'**
  String membersJoined(String when);

  /// No description provided for @membersRemove.
  ///
  /// In en, this message translates to:
  /// **'Remove'**
  String get membersRemove;

  /// No description provided for @membersRemoveTitle.
  ///
  /// In en, this message translates to:
  /// **'Remove the member?'**
  String get membersRemoveTitle;

  /// No description provided for @membersRemoveBody.
  ///
  /// In en, this message translates to:
  /// **'{email} loses access to this organization.'**
  String membersRemoveBody(String email);

  /// No description provided for @membersLeave.
  ///
  /// In en, this message translates to:
  /// **'Leave'**
  String get membersLeave;

  /// No description provided for @membersLeaveTitle.
  ///
  /// In en, this message translates to:
  /// **'Leave the organization?'**
  String get membersLeaveTitle;

  /// No description provided for @membersLeaveBody.
  ///
  /// In en, this message translates to:
  /// **'You lose access to {org}; getting back in takes a new invitation.'**
  String membersLeaveBody(String org);

  /// No description provided for @membersForbidden.
  ///
  /// In en, this message translates to:
  /// **'Your role does not allow managing members.'**
  String get membersForbidden;

  /// No description provided for @invitationsTitle.
  ///
  /// In en, this message translates to:
  /// **'Invitations'**
  String get invitationsTitle;

  /// No description provided for @invitationsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No pending invitations.'**
  String get invitationsEmpty;

  /// No description provided for @invitationsNew.
  ///
  /// In en, this message translates to:
  /// **'New invitation'**
  String get invitationsNew;

  /// No description provided for @invitationsEmail.
  ///
  /// In en, this message translates to:
  /// **'E-mail'**
  String get invitationsEmail;

  /// No description provided for @invitationsSend.
  ///
  /// In en, this message translates to:
  /// **'Send invitation'**
  String get invitationsSend;

  /// No description provided for @invitationsResend.
  ///
  /// In en, this message translates to:
  /// **'Renew'**
  String get invitationsResend;

  /// No description provided for @invitationsRevoke.
  ///
  /// In en, this message translates to:
  /// **'Revoke the invitation'**
  String get invitationsRevoke;

  /// No description provided for @invitationsExpired.
  ///
  /// In en, this message translates to:
  /// **'expired'**
  String get invitationsExpired;

  /// No description provided for @invitationsExpires.
  ///
  /// In en, this message translates to:
  /// **'expires {when}'**
  String invitationsExpires(String when);

  /// No description provided for @invitationsSentTo.
  ///
  /// In en, this message translates to:
  /// **'An invitation e-mail went to {email}.'**
  String invitationsSentTo(String email);

  /// No description provided for @invitationsNotSent.
  ///
  /// In en, this message translates to:
  /// **'The invitation for {email} was created but no e-mail could be sent; pass the link on yourself.'**
  String invitationsNotSent(String email);

  /// No description provided for @invitationsTokenOnce.
  ///
  /// In en, this message translates to:
  /// **'This code is shown only once.'**
  String get invitationsTokenOnce;

  /// No description provided for @invitationsDone.
  ///
  /// In en, this message translates to:
  /// **'Done'**
  String get invitationsDone;

  /// No description provided for @invitationsExpiredAt.
  ///
  /// In en, this message translates to:
  /// **'ended {when}'**
  String invitationsExpiredAt(String when);

  /// No description provided for @settingsLicenseKeys.
  ///
  /// In en, this message translates to:
  /// **'License keys'**
  String get settingsLicenseKeys;

  /// No description provided for @settingsApiKeys.
  ///
  /// In en, this message translates to:
  /// **'API keys'**
  String get settingsApiKeys;

  /// No description provided for @settingsBrowserKeys.
  ///
  /// In en, this message translates to:
  /// **'Browser keys'**
  String get settingsBrowserKeys;

  /// No description provided for @keysName.
  ///
  /// In en, this message translates to:
  /// **'Name'**
  String get keysName;

  /// No description provided for @keysCreate.
  ///
  /// In en, this message translates to:
  /// **'Create'**
  String get keysCreate;

  /// No description provided for @keysCreated.
  ///
  /// In en, this message translates to:
  /// **'{name} was created.'**
  String keysCreated(String name);

  /// No description provided for @keysShownOnce.
  ///
  /// In en, this message translates to:
  /// **'This value is shown only once.'**
  String get keysShownOnce;

  /// No description provided for @keysImported.
  ///
  /// In en, this message translates to:
  /// **'The value was supplied from outside, so there is nothing to show.'**
  String get keysImported;

  /// No description provided for @keysRevoke.
  ///
  /// In en, this message translates to:
  /// **'Revoke'**
  String get keysRevoke;

  /// No description provided for @keysRevoked.
  ///
  /// In en, this message translates to:
  /// **'revoked'**
  String get keysRevoked;

  /// No description provided for @keysRevokeTitle.
  ///
  /// In en, this message translates to:
  /// **'Revoke {name}?'**
  String keysRevokeTitle(String name);

  /// No description provided for @keysLastUsed.
  ///
  /// In en, this message translates to:
  /// **'last used {when}'**
  String keysLastUsed(String when);

  /// No description provided for @keysNeverUsed.
  ///
  /// In en, this message translates to:
  /// **'never used'**
  String get keysNeverUsed;

  /// No description provided for @keysExpires.
  ///
  /// In en, this message translates to:
  /// **'expires {when}'**
  String keysExpires(String when);

  /// No description provided for @keysForbidden.
  ///
  /// In en, this message translates to:
  /// **'Your role does not allow managing these keys.'**
  String get keysForbidden;

  /// No description provided for @licenseKeysEmpty.
  ///
  /// In en, this message translates to:
  /// **'No license keys.'**
  String get licenseKeysEmpty;

  /// No description provided for @licenseKeysNew.
  ///
  /// In en, this message translates to:
  /// **'New license key'**
  String get licenseKeysNew;

  /// No description provided for @licenseKeysRevokeBody.
  ///
  /// In en, this message translates to:
  /// **'Agents sending data with this key stop. The server may keep accepting it for a short while, until its auth cache expires.'**
  String get licenseKeysRevokeBody;

  /// No description provided for @apiKeysEmpty.
  ///
  /// In en, this message translates to:
  /// **'No API keys.'**
  String get apiKeysEmpty;

  /// No description provided for @apiKeysNew.
  ///
  /// In en, this message translates to:
  /// **'New API key'**
  String get apiKeysNew;

  /// No description provided for @apiKeysViewerOnly.
  ///
  /// In en, this message translates to:
  /// **'Only admins can create a key that writes.'**
  String get apiKeysViewerOnly;

  /// No description provided for @apiKeysRevokeBody.
  ///
  /// In en, this message translates to:
  /// **'Scripts using this key stop working immediately.'**
  String get apiKeysRevokeBody;

  /// No description provided for @browserKeysEmpty.
  ///
  /// In en, this message translates to:
  /// **'No browser keys.'**
  String get browserKeysEmpty;

  /// No description provided for @browserKeysNew.
  ///
  /// In en, this message translates to:
  /// **'New browser key'**
  String get browserKeysNew;

  /// No description provided for @browserKeysService.
  ///
  /// In en, this message translates to:
  /// **'Service name'**
  String get browserKeysService;

  /// No description provided for @browserKeysKindBrowser.
  ///
  /// In en, this message translates to:
  /// **'Browser'**
  String get browserKeysKindBrowser;

  /// No description provided for @browserKeysKindMobile.
  ///
  /// In en, this message translates to:
  /// **'Mobile'**
  String get browserKeysKindMobile;

  /// No description provided for @browserKeysOriginsLabel.
  ///
  /// In en, this message translates to:
  /// **'Allowed origins'**
  String get browserKeysOriginsLabel;

  /// No description provided for @browserKeysOriginsHint.
  ///
  /// In en, this message translates to:
  /// **'One per line: https://app.example.com'**
  String get browserKeysOriginsHint;

  /// No description provided for @browserKeysAppIds.
  ///
  /// In en, this message translates to:
  /// **'Allowed app ids'**
  String get browserKeysAppIds;

  /// No description provided for @browserKeysAppIdsHint.
  ///
  /// In en, this message translates to:
  /// **'One per line: com.example.shop'**
  String get browserKeysAppIdsHint;

  /// No description provided for @browserKeysOrigins.
  ///
  /// In en, this message translates to:
  /// **'{count} origins'**
  String browserKeysOrigins(int count);

  /// No description provided for @browserKeysApps.
  ///
  /// In en, this message translates to:
  /// **'{count} apps'**
  String browserKeysApps(int count);

  /// No description provided for @browserKeysRevokeBody.
  ///
  /// In en, this message translates to:
  /// **'Pages and apps sending data with this key stop.'**
  String get browserKeysRevokeBody;

  /// No description provided for @settingsSourceMaps.
  ///
  /// In en, this message translates to:
  /// **'Source maps'**
  String get settingsSourceMaps;

  /// No description provided for @settingsAuditLog.
  ///
  /// In en, this message translates to:
  /// **'Audit log'**
  String get settingsAuditLog;

  /// No description provided for @sourceMapsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No source maps stored.'**
  String get sourceMapsEmpty;

  /// No description provided for @sourceMapsUploadElsewhere.
  ///
  /// In en, this message translates to:
  /// **'Uploading happens where the file is: the build machine or CI.'**
  String get sourceMapsUploadElsewhere;

  /// No description provided for @sourceMapsSize.
  ///
  /// In en, this message translates to:
  /// **'{kb} KB'**
  String sourceMapsSize(String kb);

  /// No description provided for @sourceMapsDeleteBody.
  ///
  /// In en, this message translates to:
  /// **'Browser stacks of the build this file belongs to stop being un-minified.'**
  String get sourceMapsDeleteBody;

  /// No description provided for @auditActor.
  ///
  /// In en, this message translates to:
  /// **'Who'**
  String get auditActor;

  /// No description provided for @auditAction.
  ///
  /// In en, this message translates to:
  /// **'Action'**
  String get auditAction;

  /// No description provided for @auditActionHint.
  ///
  /// In en, this message translates to:
  /// **'A prefix: member. or member.remove'**
  String get auditActionHint;

  /// No description provided for @auditEmpty.
  ///
  /// In en, this message translates to:
  /// **'No entries with this filter.'**
  String get auditEmpty;

  /// No description provided for @auditMore.
  ///
  /// In en, this message translates to:
  /// **'Older'**
  String get auditMore;

  /// No description provided for @auditEnd.
  ///
  /// In en, this message translates to:
  /// **'End of the log.'**
  String get auditEnd;

  /// No description provided for @auditUnknownActor.
  ///
  /// In en, this message translates to:
  /// **'unknown'**
  String get auditUnknownActor;

  /// No description provided for @auditWithKey.
  ///
  /// In en, this message translates to:
  /// **'with an API key'**
  String get auditWithKey;

  /// No description provided for @auditForbidden.
  ///
  /// In en, this message translates to:
  /// **'Your role does not allow reading the audit log.'**
  String get auditForbidden;

  /// No description provided for @settingsOrganization.
  ///
  /// In en, this message translates to:
  /// **'Organization'**
  String get settingsOrganization;

  /// No description provided for @orgName.
  ///
  /// In en, this message translates to:
  /// **'Name'**
  String get orgName;

  /// No description provided for @orgRename.
  ///
  /// In en, this message translates to:
  /// **'Rename'**
  String get orgRename;

  /// No description provided for @orgCreated.
  ///
  /// In en, this message translates to:
  /// **'Created'**
  String get orgCreated;

  /// No description provided for @orgTenantId.
  ///
  /// In en, this message translates to:
  /// **'Tenant id'**
  String get orgTenantId;

  /// No description provided for @orgId.
  ///
  /// In en, this message translates to:
  /// **'Organization id'**
  String get orgId;

  /// No description provided for @orgLanguage.
  ///
  /// In en, this message translates to:
  /// **'Organization language'**
  String get orgLanguage;

  /// No description provided for @orgLanguageHint.
  ///
  /// In en, this message translates to:
  /// **'The language the server writes in for people who have not chosen one.'**
  String get orgLanguageHint;

  /// No description provided for @orgLanguageNone.
  ///
  /// In en, this message translates to:
  /// **'Not chosen'**
  String get orgLanguageNone;

  /// No description provided for @orgForbidden.
  ///
  /// In en, this message translates to:
  /// **'Your role does not allow changing the organization.'**
  String get orgForbidden;

  /// No description provided for @settingsSampling.
  ///
  /// In en, this message translates to:
  /// **'APM sampling'**
  String get settingsSampling;

  /// No description provided for @settingsUsage.
  ///
  /// In en, this message translates to:
  /// **'Usage and plan'**
  String get settingsUsage;

  /// No description provided for @usageCurrent.
  ///
  /// In en, this message translates to:
  /// **'This period'**
  String get usageCurrent;

  /// No description provided for @usagePrevious.
  ///
  /// In en, this message translates to:
  /// **'Previous period'**
  String get usagePrevious;

  /// No description provided for @usageSaas.
  ///
  /// In en, this message translates to:
  /// **'SaaS'**
  String get usageSaas;

  /// No description provided for @usageSelfHosted.
  ///
  /// In en, this message translates to:
  /// **'Self-hosted'**
  String get usageSelfHosted;

  /// No description provided for @usagePlanDefault.
  ///
  /// In en, this message translates to:
  /// **'No plan assigned; the catalog default applies.'**
  String get usagePlanDefault;

  /// No description provided for @usageBlocked.
  ///
  /// In en, this message translates to:
  /// **'Ingest is blocked: the plan limit was exceeded.'**
  String get usageBlocked;

  /// No description provided for @usageIngest.
  ///
  /// In en, this message translates to:
  /// **'Ingest'**
  String get usageIngest;

  /// No description provided for @usageHosts.
  ///
  /// In en, this message translates to:
  /// **'Hosts'**
  String get usageHosts;

  /// No description provided for @usageUsers.
  ///
  /// In en, this message translates to:
  /// **'Users'**
  String get usageUsers;

  /// No description provided for @usageContainers.
  ///
  /// In en, this message translates to:
  /// **'Containers'**
  String get usageContainers;

  /// No description provided for @usageServices.
  ///
  /// In en, this message translates to:
  /// **'Services'**
  String get usageServices;

  /// No description provided for @usageQueries.
  ///
  /// In en, this message translates to:
  /// **'Queries'**
  String get usageQueries;

  /// No description provided for @usageStored.
  ///
  /// In en, this message translates to:
  /// **'Stored (compressed)'**
  String get usageStored;

  /// No description provided for @usageUnlimited.
  ///
  /// In en, this message translates to:
  /// **'Unlimited'**
  String get usageUnlimited;

  /// No description provided for @usageProjected.
  ///
  /// In en, this message translates to:
  /// **'Projected at period end: {value}'**
  String usageProjected(String value);

  /// No description provided for @usageProjectedPercent.
  ///
  /// In en, this message translates to:
  /// **'Projected at period end: {value} ({percent}% of the limit)'**
  String usageProjectedPercent(String value, int percent);

  /// No description provided for @usageBySignal.
  ///
  /// In en, this message translates to:
  /// **'By signal'**
  String get usageBySignal;

  /// No description provided for @usageRetention.
  ///
  /// In en, this message translates to:
  /// **'Retention'**
  String get usageRetention;

  /// No description provided for @usageDays.
  ///
  /// In en, this message translates to:
  /// **'{count} days'**
  String usageDays(int count);

  /// No description provided for @usageForbidden.
  ///
  /// In en, this message translates to:
  /// **'Your role does not allow seeing usage.'**
  String get usageForbidden;

  /// No description provided for @settingsStorage.
  ///
  /// In en, this message translates to:
  /// **'Storage'**
  String get settingsStorage;

  /// No description provided for @storageNotMeasured.
  ///
  /// In en, this message translates to:
  /// **'The disks have not been measured yet.'**
  String get storageNotMeasured;

  /// No description provided for @storageLevels.
  ///
  /// In en, this message translates to:
  /// **'Warning at {warn}%, high at {high}%.'**
  String storageLevels(int warn, int high);

  /// No description provided for @storageFree.
  ///
  /// In en, this message translates to:
  /// **'{free} free of {total}'**
  String storageFree(String free, String total);

  /// No description provided for @storageBroken.
  ///
  /// In en, this message translates to:
  /// **'The disk cannot be read.'**
  String get storageBroken;

  /// No description provided for @storageForbidden.
  ///
  /// In en, this message translates to:
  /// **'Your role does not allow seeing disk status.'**
  String get storageForbidden;

  /// No description provided for @settingsSso.
  ///
  /// In en, this message translates to:
  /// **'SSO'**
  String get settingsSso;

  /// No description provided for @ssoUnavailable.
  ///
  /// In en, this message translates to:
  /// **'SSO cannot be used: the server\'s public URL (OPENLOG_PUBLIC_URL) is not set.'**
  String get ssoUnavailable;

  /// No description provided for @ssoSecretsPlain.
  ///
  /// In en, this message translates to:
  /// **'Secrets are stored unencrypted; set a key on the server.'**
  String get ssoSecretsPlain;

  /// No description provided for @ssoConnections.
  ///
  /// In en, this message translates to:
  /// **'Connections'**
  String get ssoConnections;

  /// No description provided for @ssoNoConnections.
  ///
  /// In en, this message translates to:
  /// **'No SSO connections.'**
  String get ssoNoConnections;

  /// No description provided for @ssoEditOnWeb.
  ///
  /// In en, this message translates to:
  /// **'Creating and editing a connection is on the web: it means pasting a metadata URL, a client secret and a certificate.'**
  String get ssoEditOnWeb;

  /// No description provided for @ssoTestOk.
  ///
  /// In en, this message translates to:
  /// **'The server-side checks passed.'**
  String get ssoTestOk;

  /// No description provided for @ssoTestFailed.
  ///
  /// In en, this message translates to:
  /// **'Checks that failed: {checks}'**
  String ssoTestFailed(String checks);

  /// No description provided for @ssoEnforce.
  ///
  /// In en, this message translates to:
  /// **'SSO required'**
  String get ssoEnforce;

  /// No description provided for @ssoEnforceHint.
  ///
  /// In en, this message translates to:
  /// **'On, everybody signs in through the identity provider.'**
  String get ssoEnforceHint;

  /// No description provided for @ssoBreakGlass.
  ///
  /// In en, this message translates to:
  /// **'{count} people can still sign in with a password.'**
  String ssoBreakGlass(int count);

  /// No description provided for @ssoNoBreakGlass.
  ///
  /// In en, this message translates to:
  /// **'Nobody is allowed to sign in with a password.'**
  String get ssoNoBreakGlass;

  /// No description provided for @ssoDomains.
  ///
  /// In en, this message translates to:
  /// **'Domains'**
  String get ssoDomains;

  /// No description provided for @ssoDomainsHint.
  ///
  /// In en, this message translates to:
  /// **'E-mail addresses in these domains sign in through SSO.'**
  String get ssoDomainsHint;

  /// No description provided for @ssoDomainAdd.
  ///
  /// In en, this message translates to:
  /// **'Add a domain'**
  String get ssoDomainAdd;

  /// No description provided for @ssoVerified.
  ///
  /// In en, this message translates to:
  /// **'verified'**
  String get ssoVerified;

  /// No description provided for @ssoUnverified.
  ///
  /// In en, this message translates to:
  /// **'unverified'**
  String get ssoUnverified;

  /// No description provided for @ssoVerifyDns.
  ///
  /// In en, this message translates to:
  /// **'Verify by DNS'**
  String get ssoVerifyDns;

  /// No description provided for @ssoVerifyEmail.
  ///
  /// In en, this message translates to:
  /// **'Verify by e-mail'**
  String get ssoVerifyEmail;

  /// No description provided for @ssoRoleMappings.
  ///
  /// In en, this message translates to:
  /// **'Group mappings'**
  String get ssoRoleMappings;

  /// No description provided for @ssoRoleMappingsHint.
  ///
  /// In en, this message translates to:
  /// **'A group at the identity provider becomes a role here. Owner is not given by a mapping.'**
  String get ssoRoleMappingsHint;

  /// No description provided for @ssoGroup.
  ///
  /// In en, this message translates to:
  /// **'Group'**
  String get ssoGroup;

  /// No description provided for @ssoScimTokens.
  ///
  /// In en, this message translates to:
  /// **'SCIM tokens'**
  String get ssoScimTokens;

  /// No description provided for @ssoNoScimTokens.
  ///
  /// In en, this message translates to:
  /// **'No SCIM tokens.'**
  String get ssoNoScimTokens;

  /// No description provided for @ssoForbidden.
  ///
  /// In en, this message translates to:
  /// **'Your role does not allow seeing the SSO settings.'**
  String get ssoForbidden;

  /// No description provided for @ssoTest.
  ///
  /// In en, this message translates to:
  /// **'Test the connection'**
  String get ssoTest;

  /// No description provided for @ssoHealthOk.
  ///
  /// In en, this message translates to:
  /// **'Healthy'**
  String get ssoHealthOk;

  /// No description provided for @ssoHealthWarning.
  ///
  /// In en, this message translates to:
  /// **'Warning'**
  String get ssoHealthWarning;

  /// No description provided for @ssoHealthError.
  ///
  /// In en, this message translates to:
  /// **'Error'**
  String get ssoHealthError;

  /// No description provided for @ssoHealthUnknown.
  ///
  /// In en, this message translates to:
  /// **'Not checked yet'**
  String get ssoHealthUnknown;

  /// No description provided for @privacySsoReauth.
  ///
  /// In en, this message translates to:
  /// **'This account has no password; the server wants a recent single sign-on instead.'**
  String get privacySsoReauth;

  /// No description provided for @privacyDelete.
  ///
  /// In en, this message translates to:
  /// **'Delete'**
  String get privacyDelete;

  /// No description provided for @privacyUnverified.
  ///
  /// In en, this message translates to:
  /// **'{email} is not verified.'**
  String privacyUnverified(String email);

  /// No description provided for @privacyResend.
  ///
  /// In en, this message translates to:
  /// **'Send the verification e-mail again'**
  String get privacyResend;

  /// No description provided for @privacyVerificationSent.
  ///
  /// In en, this message translates to:
  /// **'The verification e-mail went out.'**
  String get privacyVerificationSent;

  /// No description provided for @privacyExport.
  ///
  /// In en, this message translates to:
  /// **'A copy of my data'**
  String get privacyExport;

  /// No description provided for @privacyExportHint.
  ///
  /// In en, this message translates to:
  /// **'Downloaded from the web once it is ready; a phone is not where anybody opens an archive.'**
  String get privacyExportHint;

  /// No description provided for @privacyExportRequest.
  ///
  /// In en, this message translates to:
  /// **'Request a copy'**
  String get privacyExportRequest;

  /// No description provided for @privacyExportQueued.
  ///
  /// In en, this message translates to:
  /// **'Requested; an e-mail arrives when it is ready.'**
  String get privacyExportQueued;

  /// No description provided for @privacyOrgDeletion.
  ///
  /// In en, this message translates to:
  /// **'{org} is scheduled for deletion: it goes permanently {when}.'**
  String privacyOrgDeletion(String org, String when);

  /// No description provided for @privacyCancelDeletion.
  ///
  /// In en, this message translates to:
  /// **'Cancel the deletion'**
  String get privacyCancelDeletion;

  /// No description provided for @privacyDangerous.
  ///
  /// In en, this message translates to:
  /// **'Cannot be undone'**
  String get privacyDangerous;

  /// No description provided for @privacyDeleteOrg.
  ///
  /// In en, this message translates to:
  /// **'Delete the organization'**
  String get privacyDeleteOrg;

  /// No description provided for @privacyDeleteOrgTitle.
  ///
  /// In en, this message translates to:
  /// **'Delete the organization'**
  String get privacyDeleteOrgTitle;

  /// No description provided for @privacyDeleteOrgBody.
  ///
  /// In en, this message translates to:
  /// **'{org} and everything in it is deleted permanently after {days} days. You can cancel until then. Type its name to confirm.'**
  String privacyDeleteOrgBody(String org, int days);

  /// No description provided for @privacyDeleteAccount.
  ///
  /// In en, this message translates to:
  /// **'Delete my account'**
  String get privacyDeleteAccount;

  /// No description provided for @privacyDeleteAccountTitle.
  ///
  /// In en, this message translates to:
  /// **'Delete the account'**
  String get privacyDeleteAccountTitle;

  /// No description provided for @privacyDeleteAccountBody.
  ///
  /// In en, this message translates to:
  /// **'Your account and your personal data are deleted. Type your e-mail address to confirm.'**
  String get privacyDeleteAccountBody;

  /// No description provided for @alreadyVerified.
  ///
  /// In en, this message translates to:
  /// **'This address is already verified.'**
  String get alreadyVerified;

  /// No description provided for @reauthNeeded.
  ///
  /// In en, this message translates to:
  /// **'Prove it is you: the password was wrong, or the single sign-on is too old.'**
  String get reauthNeeded;

  /// No description provided for @filterAdd.
  ///
  /// In en, this message translates to:
  /// **'Filter'**
  String get filterAdd;

  /// No description provided for @filterPickKey.
  ///
  /// In en, this message translates to:
  /// **'Pick a field'**
  String get filterPickKey;

  /// No description provided for @filterSearchKey.
  ///
  /// In en, this message translates to:
  /// **'Search fields'**
  String get filterSearchKey;

  /// No description provided for @filterSearchValue.
  ///
  /// In en, this message translates to:
  /// **'Search values'**
  String get filterSearchValue;

  /// No description provided for @filterBack.
  ///
  /// In en, this message translates to:
  /// **'Back to the fields'**
  String get filterBack;

  /// No description provided for @filterExists.
  ///
  /// In en, this message translates to:
  /// **'Only where it exists'**
  String get filterExists;

  /// No description provided for @filterApply.
  ///
  /// In en, this message translates to:
  /// **'Apply'**
  String get filterApply;

  /// No description provided for @filterRecords.
  ///
  /// In en, this message translates to:
  /// **'{count} records'**
  String filterRecords(int count);

  /// No description provided for @filterDistinct.
  ///
  /// In en, this message translates to:
  /// **'{count} distinct values'**
  String filterDistinct(int count);

  /// No description provided for @filterEmptyValue.
  ///
  /// In en, this message translates to:
  /// **'(empty)'**
  String get filterEmptyValue;

  /// No description provided for @filterSampled.
  ///
  /// In en, this message translates to:
  /// **'Counted from a sample; the most frequent are listed.'**
  String get filterSampled;

  /// No description provided for @logsTabRecords.
  ///
  /// In en, this message translates to:
  /// **'Records'**
  String get logsTabRecords;

  /// No description provided for @logsTabPatterns.
  ///
  /// In en, this message translates to:
  /// **'Patterns'**
  String get logsTabPatterns;

  /// No description provided for @patternsEmpty.
  ///
  /// In en, this message translates to:
  /// **'No patterns with this filter.'**
  String get patternsEmpty;

  /// No description provided for @patternsTotal.
  ///
  /// In en, this message translates to:
  /// **'{count} records'**
  String patternsTotal(int count);

  /// No description provided for @patternsUnclassified.
  ///
  /// In en, this message translates to:
  /// **'{count} without a pattern'**
  String patternsUnclassified(int count);

  /// No description provided for @patternsRollup.
  ///
  /// In en, this message translates to:
  /// **'from the hourly rollup'**
  String get patternsRollup;

  /// No description provided for @patternsTruncated.
  ///
  /// In en, this message translates to:
  /// **'Not every pattern; narrow the filter.'**
  String get patternsTruncated;

  /// No description provided for @patternsErrors.
  ///
  /// In en, this message translates to:
  /// **'{count} errors'**
  String patternsErrors(int count);

  /// No description provided for @volumeTotal.
  ///
  /// In en, this message translates to:
  /// **'{count} records · {step} buckets'**
  String volumeTotal(int count, String step);

  /// No description provided for @volumeP95.
  ///
  /// In en, this message translates to:
  /// **'p95 {ms} ms'**
  String volumeP95(String ms);

  /// No description provided for @metricExemplars.
  ///
  /// In en, this message translates to:
  /// **'Sample traces'**
  String get metricExemplars;

  /// No description provided for @metricExemplarsLoad.
  ///
  /// In en, this message translates to:
  /// **'Fetch the traces behind this metric'**
  String get metricExemplarsLoad;

  /// No description provided for @metricExemplarsFailed.
  ///
  /// In en, this message translates to:
  /// **'The sample traces could not be read: {message}'**
  String metricExemplarsFailed(String message);

  /// No description provided for @metricExemplarsTruncated.
  ///
  /// In en, this message translates to:
  /// **'Not all of them; the range holds more.'**
  String get metricExemplarsTruncated;

  /// No description provided for @correlationsTitle.
  ///
  /// In en, this message translates to:
  /// **'What else changed'**
  String get correlationsTitle;

  /// No description provided for @correlationsAbout.
  ///
  /// In en, this message translates to:
  /// **'Series that behaved differently during the incident\'s window than before it.'**
  String get correlationsAbout;

  /// No description provided for @correlationsLoad.
  ///
  /// In en, this message translates to:
  /// **'Compute'**
  String get correlationsLoad;

  /// No description provided for @correlationsFailed.
  ///
  /// In en, this message translates to:
  /// **'Could not be computed: {message}'**
  String correlationsFailed(String message);

  /// No description provided for @correlationsNoRatio.
  ///
  /// In en, this message translates to:
  /// **'no ratio'**
  String get correlationsNoRatio;

  /// No description provided for @correlationsMeans.
  ///
  /// In en, this message translates to:
  /// **'before {baseline} → after {window} · score {score}'**
  String correlationsMeans(String baseline, String window, String score);
}

class _LDelegate extends LocalizationsDelegate<L> {
  const _LDelegate();

  @override
  Future<L> load(Locale locale) {
    return SynchronousFuture<L>(lookupL(locale));
  }

  @override
  bool isSupported(Locale locale) =>
      <String>['en', 'tr'].contains(locale.languageCode);

  @override
  bool shouldReload(_LDelegate old) => false;
}

L lookupL(Locale locale) {
  // Lookup logic when only language code is specified.
  switch (locale.languageCode) {
    case 'en':
      return LEn();
    case 'tr':
      return LTr();
  }

  throw FlutterError(
    'L.delegate failed to load unsupported locale "$locale". This is likely '
    'an issue with the localizations generation tool. Please file an issue '
    'on GitHub with a reproducible sample app and the gen-l10n configuration '
    'that was used.',
  );
}
