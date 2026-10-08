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
  String get accountTitle => 'Account';

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
  String get navServices => 'Services';

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
}
