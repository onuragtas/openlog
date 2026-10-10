// What this account may ask for: a copy of its data, and its own deletion.
//
// The web's personal data section, on the profile tab. The two destructive
// ones ask for the name or the address again, as the server does, and the
// deletions it already scheduled are listed with the way to cancel them --
// which is the only reason somebody opens this on a phone.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../account.dart';
import '../api/schema.g.dart';
import '../session.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'severity.dart';

class PrivacyCard extends StatelessWidget {
  const PrivacyCard({
    super.key,
    required this.session,
    required this.controller,
  });

  final SessionController session;
  final PrivacyController controller;

  /// Asks for the password, where the account has one, and for the exact
  /// name or address -- both because the server insists and because this
  /// is the kind of thing nobody should do by mistyping.
  Future<({String confirm, String password})?> _confirm(
    BuildContext context, {
    required String title,
    required String body,
    required String expected,
    required String label,
    required bool needsPassword,
  }) async {
    final l = L.of(context);
    final confirm = TextEditingController();
    final password = TextEditingController();
    final result = await showDialog<({String confirm, String password})>(
      context: context,
      builder: (context) => StatefulBuilder(
        builder: (context, setState) => AlertDialog(
          title: Text(title),
          content: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(body, style: Theme.of(context).textTheme.bodySmall),
              const SizedBox(height: 10),
              TextField(
                key: const Key('privacy-confirm'),
                controller: confirm,
                autocorrect: false,
                onChanged: (_) => setState(() {}),
                decoration: InputDecoration(
                  labelText: label,
                  border: const OutlineInputBorder(),
                  isDense: true,
                ),
              ),
              if (needsPassword) ...[
                const SizedBox(height: 10),
                TextField(
                  key: const Key('privacy-password'),
                  controller: password,
                  obscureText: true,
                  onChanged: (_) => setState(() {}),
                  decoration: InputDecoration(
                    labelText: l.securityCurrentPassword,
                    border: const OutlineInputBorder(),
                    isDense: true,
                  ),
                ),
              ] else
                Padding(
                  padding: const EdgeInsets.only(top: 8),
                  child: Text(
                    // No password on this account: the server wants a
                    // recent single sign-on instead, which is not
                    // something this dialog can ask for.
                    l.privacySsoReauth,
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                ),
            ],
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.of(context).pop(),
              child: Text(l.ruleCancel),
            ),
            FilledButton(
              key: const Key('privacy-confirm-ok'),
              onPressed:
                  confirm.text.trim() != expected ||
                      (needsPassword && password.text.isEmpty)
                  ? null
                  : () => Navigator.of(context).pop((
                      confirm: confirm.text.trim(),
                      password: password.text,
                    )),
              child: Text(l.privacyDelete),
            ),
          ],
        ),
      ),
    );
    confirm.dispose();
    password.dispose();
    return result;
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) {
        final p = c.privacy;
        final me = session.me?.user;
        final verified = me?.emailVerified ?? true;

        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            FailureBanner(failure: c.failure, baseUrl: session.baseUrl ?? ''),
            if (!verified)
              Card(
                key: const Key('privacy-verify'),
                child: Padding(
                  padding: const EdgeInsets.all(14),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        l.privacyUnverified(me?.email ?? ''),
                        style: theme.textTheme.bodyMedium,
                      ),
                      if (c.verificationSent)
                        Text(
                          l.privacyVerificationSent,
                          style: theme.textTheme.bodySmall?.copyWith(
                            color: severityTextColor(
                              context,
                              SeverityLevel.good,
                            ),
                          ),
                        ),
                      Align(
                        alignment: Alignment.centerRight,
                        child: TextButton(
                          key: const Key('privacy-resend'),
                          onPressed: c.busy ? null : c.resendVerification,
                          child: Text(l.privacyResend),
                        ),
                      ),
                    ],
                  ),
                ),
              ),
            if (p != null) ...[
              const SizedBox(height: 12),
              // Deletions already scheduled, and the way to call one off.
              // This is the one thing on this card somebody opens a phone
              // for.
              for (final d in p.orgDeletions)
                Card(
                  key: Key('org-deletion-${d.id}'),
                  child: Padding(
                    padding: const EdgeInsets.all(14),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          l.privacyOrgDeletion(
                            d.organizationName,
                            whenOf(d.purgeAfter, (t) => relativeTimeOf(l, t)),
                          ),
                          style: theme.textTheme.bodyMedium?.copyWith(
                            color: severityTextColor(
                              context,
                              SeverityLevel.critical,
                            ),
                          ),
                        ),
                        if (d.cancellable)
                          Align(
                            alignment: Alignment.centerRight,
                            child: TextButton(
                              key: Key('org-deletion-cancel-${d.id}'),
                              onPressed: c.busy
                                  ? null
                                  : () => c.cancelOrgDeletion(d.id),
                              child: Text(l.privacyCancelDeletion),
                            ),
                          ),
                      ],
                    ),
                  ),
                ),
              if (p.dataExportEnabled)
                Card(
                  child: Padding(
                    padding: const EdgeInsets.all(14),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          l.privacyExport,
                          style: theme.textTheme.titleSmall,
                        ),
                        Text(
                          // The file is downloaded from the web: a phone
                          // is not where anybody opens an archive.
                          l.privacyExportHint,
                          style: theme.textTheme.bodySmall?.copyWith(
                            color: theme.colorScheme.onSurfaceVariant,
                          ),
                        ),
                        for (final e in c.exports.take(3))
                          Text(
                            '${e.status.wire} · '
                            '${whenOf(e.createdAt, (t) => relativeTimeOf(l, t))}',
                            style: theme.textTheme.bodySmall,
                          ),
                        if (c.exportQueued)
                          Text(
                            l.privacyExportQueued,
                            key: const Key('privacy-export-queued'),
                            style: theme.textTheme.bodySmall?.copyWith(
                              color: severityTextColor(
                                context,
                                SeverityLevel.good,
                              ),
                            ),
                          ),
                        Align(
                          alignment: Alignment.centerRight,
                          child: TextButton(
                            key: const Key('privacy-export'),
                            onPressed: c.busy ? null : c.requestExport,
                            child: Text(l.privacyExportRequest),
                          ),
                        ),
                      ],
                    ),
                  ),
                ),
              const SizedBox(height: 12),
              Card(
                child: Padding(
                  padding: const EdgeInsets.all(14),
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        l.privacyDangerous,
                        style: theme.textTheme.titleSmall?.copyWith(
                          color: severityTextColor(
                            context,
                            SeverityLevel.critical,
                          ),
                        ),
                      ),
                      if (session.me?.role == Role.owner)
                        Align(
                          alignment: Alignment.centerLeft,
                          child: TextButton(
                            key: const Key('privacy-delete-org'),
                            onPressed: c.busy
                                ? null
                                : () async {
                                    final org = session.organization;
                                    if (org == null) return;
                                    final answer = await _confirm(
                                      context,
                                      title: l.privacyDeleteOrgTitle,
                                      body: l.privacyDeleteOrgBody(
                                        org.name,
                                        (p.orgDeletionGraceSeconds / 86400)
                                            .round(),
                                      ),
                                      expected: org.name,
                                      label: l.orgName,
                                      needsPassword: p.hasPassword,
                                    );
                                    if (answer == null) return;
                                    await c.deleteOrganization(
                                      confirmName: answer.confirm,
                                      password: answer.password,
                                    );
                                  },
                            child: Text(l.privacyDeleteOrg),
                          ),
                        ),
                      Align(
                        alignment: Alignment.centerLeft,
                        child: TextButton(
                          key: const Key('privacy-delete-account'),
                          onPressed: c.busy
                              ? null
                              : () async {
                                  final email = me?.email ?? '';
                                  final answer = await _confirm(
                                    context,
                                    title: l.privacyDeleteAccountTitle,
                                    body: l.privacyDeleteAccountBody,
                                    expected: email,
                                    label: l.invitationsEmail,
                                    needsPassword: p.hasPassword,
                                  );
                                  if (answer == null) return;
                                  await c.deleteAccount(
                                    confirmEmail: answer.confirm,
                                    password: answer.password,
                                    // The token belongs to an account
                                    // that no longer exists.
                                    signOut: session.signOut,
                                  );
                                },
                          child: Text(l.privacyDeleteAccount),
                        ),
                      ),
                    ],
                  ),
                ),
              ),
            ],
          ],
        );
      },
    );
  }
}
