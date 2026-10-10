// One place that turns a SessionFailure into something a person can read, so
// three screens cannot word the same problem three ways.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../session.dart';

/// The sentence to show for [failure], or null when there is nothing to say.
String? failureText(
  BuildContext context,
  SessionFailure? failure, {
  required String baseUrl,
}) {
  if (failure == null) return null;
  final l = L.of(context);
  switch (failure.kind) {
    case 'badAddress':
      return l.serverInvalidAddress;
    case 'unreachable':
      return l.serverUnreachable(baseUrl);
    case 'notOpenlog':
      return l.serverNotOpenlog;
    case 'badCredentials':
      return l.signInInvalid;
    case 'rateLimited':
      return l.signInRateLimited;
    case 'signUpClosed':
      return l.signUpDisabled;
    case 'signUpNeedsCaptcha':
      return l.signUpOnWeb(baseUrl);
    case 'emailTaken':
      return l.signUpEmailTaken;
    // Every forbidden kind a controller can produce has to be named here. They
    // were not, and a member without the APM permission was told "something
    // went wrong:" with nothing after the colon, which reads like a bug in the
    // app rather than a permission they do not have.
    case 'ssoForbidden':
      return l.ssoForbidden;
    case 'storageForbidden':
      return l.storageForbidden;
    case 'usageForbidden':
      return l.usageForbidden;
    case 'orgForbidden':
      return l.orgForbidden;
    case 'auditForbidden':
      return l.auditForbidden;
    case 'keysForbidden':
      return l.keysForbidden;
    case 'membersForbidden':
      return l.membersForbidden;
    case 'apdexUnavailable':
      return l.apdexUnavailable;
    case 'samplingConflict':
      return l.samplingConflict;
    case 'samplingUnavailable':
      return l.samplingUnavailable;
    case 'alertsForbidden':
      return l.alertsForbidden;
    case 'servicesForbidden':
      return l.servicesForbidden;
    case 'logsForbidden':
      return l.logsForbidden;
    case 'dashboardsForbidden':
      return l.dashboardsForbidden;
    case 'sectionForbidden':
      return l.sectionForbidden;
    case 'alreadyResolved':
      return l.alertsAlreadyResolved;
    case 'detailGone':
      return l.detailGone;
    case 'onboardingForbidden':
      return l.onboardingForbidden;
    case 'channelsNoSecrets':
      return l.channelsNoSecrets;
    case 'costsOff':
      return l.costsOff;
    case 'queryForbidden':
      return l.queryForbidden;
    case 'queryRejected':
      // The server's own words, which on this screen are the useful ones: the
      // person is writing OQL and the message says what is wrong with it.
      return l.queryRejected(failure.detail);
    default:
      // The server's own message, which is more useful than anything this app
      // could invent about a problem it does not recognise.
      return l.errorUnexpected(failure.detail);
  }
}

/// A red block under a form. Nothing when there is no failure, so the layout
/// does not keep space for a message that is not there.
class FailureBanner extends StatelessWidget {
  const FailureBanner({
    super.key,
    required this.failure,
    required this.baseUrl,
  });

  final SessionFailure? failure;
  final String baseUrl;

  @override
  Widget build(BuildContext context) {
    final text = failureText(context, failure, baseUrl: baseUrl);
    if (text == null) return const SizedBox.shrink();
    final scheme = Theme.of(context).colorScheme;
    return Container(
      margin: const EdgeInsets.only(top: 16),
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: scheme.errorContainer,
        borderRadius: BorderRadius.circular(8),
      ),
      child: Text(text, style: TextStyle(color: scheme.onErrorContainer)),
    );
  }
}
