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

  /// No description provided for @accountTitle.
  ///
  /// In en, this message translates to:
  /// **'Account'**
  String get accountTitle;

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

  /// No description provided for @navServices.
  ///
  /// In en, this message translates to:
  /// **'Services'**
  String get navServices;

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
