// Telling one discovered integration how to reach its service.
//
// The web shows this form under an integration that needs configuring or
// has failed: the endpoint, the user, the password and the database, as
// far as that integration uses them. Same fields here, in a sheet.
//
// The password is write-only on both sides: an empty box keeps what is
// stored, and clearing it is a separate choice -- otherwise opening the
// form and saving would wipe a password nobody meant to touch.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../integration_settings.dart';
import '../session.dart';
import 'failure_text.dart';
import 'severity.dart';

/// Opens the form for one instance. Returns true when something was
/// saved, so the caller can say the change is on its way.
Future<bool> editIntegration(
  BuildContext context, {
  required SessionController session,
  required IntegrationSettingsController controller,
  required String integration,
  required String instance,
  required String hostName,
}) async {
  if (!controller.loading && controller.host == null) {
    await controller.load();
  }
  if (!context.mounted) return false;
  final saved = await showModalBottomSheet<bool>(
    context: context,
    isScrollControlled: true,
    useSafeArea: true,
    builder: (context) => _ConfigSheet(
      session: session,
      controller: controller,
      integration: integration,
      instance: instance,
      hostName: hostName,
    ),
  );
  return saved ?? false;
}

class _ConfigSheet extends StatefulWidget {
  const _ConfigSheet({
    required this.session,
    required this.controller,
    required this.integration,
    required this.instance,
    required this.hostName,
  });

  final SessionController session;
  final IntegrationSettingsController controller;
  final String integration;
  final String instance;
  final String hostName;

  @override
  State<_ConfigSheet> createState() => _ConfigSheetState();
}

class _ConfigSheetState extends State<_ConfigSheet> {
  late final IntegrationSetting? _existing = widget.controller.settingFor(
    widget.integration,
    widget.instance,
  );
  late final _endpoint = TextEditingController(text: _existing?.endpoint ?? '');
  late final _username = TextEditingController(text: _existing?.username ?? '');
  late final _database = TextEditingController(text: _existing?.database ?? '');
  final _password = TextEditingController();
  late bool _enabled = _existing?.enabled ?? true;
  bool _clearPassword = false;

  @override
  void dispose() {
    _endpoint.dispose();
    _username.dispose();
    _database.dispose();
    _password.dispose();
    super.dispose();
  }

  Future<void> _save() async {
    final ok = await widget.controller.save(
      integration: widget.integration,
      instance: widget.instance,
      enabled: _enabled,
      endpoint: _endpoint.text,
      username: _username.text,
      database: _database.text,
      password: _password.text,
      clearPassword: _clearPassword,
    );
    if (!ok || !mounted) return;
    Navigator.of(context).pop(true);
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.controller;
    final fields = configFields[widget.integration] ?? const [];

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => Padding(
        padding: EdgeInsets.only(
          left: 16,
          right: 16,
          top: 16,
          bottom: MediaQuery.viewInsetsOf(context).bottom + 16,
        ),
        child: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(widget.integration, style: theme.textTheme.titleMedium),
              Text(
                '${widget.hostName} · ${widget.instance}',
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
              FailureBanner(
                failure: c.failure,
                baseUrl: widget.session.baseUrl ?? '',
              ),
              if (c.host?.remoteConfigDisabled ?? false)
                Padding(
                  padding: const EdgeInsets.only(top: 8),
                  child: Text(
                    // Saving would be theatre: the agent is not listening.
                    l.integRemoteOff,
                    key: const Key('integration-remote-off'),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: severityTextColor(context, SeverityLevel.warning),
                    ),
                  ),
                ),
              const SizedBox(height: 12),
              SwitchListTile(
                key: const Key('integration-enabled'),
                contentPadding: EdgeInsets.zero,
                dense: true,
                title: Text(l.integCollect),
                value: _enabled,
                onChanged: (v) => setState(() => _enabled = v),
              ),
              if (fields.contains('endpoint')) ...[
                const SizedBox(height: 8),
                TextField(
                  key: const Key('integration-endpoint'),
                  controller: _endpoint,
                  autocorrect: false,
                  keyboardType: TextInputType.url,
                  decoration: InputDecoration(
                    labelText: l.integEndpoint,
                    hintText: endpointPlaceholders[widget.integration],
                    border: const OutlineInputBorder(),
                    isDense: true,
                  ),
                ),
              ],
              if (fields.contains('username')) ...[
                const SizedBox(height: 8),
                TextField(
                  key: const Key('integration-username'),
                  controller: _username,
                  autocorrect: false,
                  decoration: InputDecoration(
                    labelText: l.integUsername,
                    border: const OutlineInputBorder(),
                    isDense: true,
                  ),
                ),
              ],
              if (fields.contains('password')) ...[
                const SizedBox(height: 8),
                TextField(
                  key: const Key('integration-password'),
                  controller: _password,
                  obscureText: true,
                  autocorrect: false,
                  enableSuggestions: false,
                  enabled: !_clearPassword,
                  decoration: InputDecoration(
                    labelText: l.integPassword,
                    // What an empty box means, said rather than assumed.
                    helperText: _existing?.passwordSet ?? false
                        ? l.integPasswordKeep
                        : null,
                    helperMaxLines: 2,
                    border: const OutlineInputBorder(),
                    isDense: true,
                  ),
                ),
                if (_existing?.passwordSet ?? false)
                  CheckboxListTile(
                    key: const Key('integration-clear-password'),
                    contentPadding: EdgeInsets.zero,
                    dense: true,
                    controlAffinity: ListTileControlAffinity.leading,
                    title: Text(l.integPasswordClear),
                    value: _clearPassword,
                    onChanged: (v) =>
                        setState(() => _clearPassword = v ?? false),
                  ),
              ],
              if (fields.contains('database')) ...[
                const SizedBox(height: 8),
                TextField(
                  key: const Key('integration-database'),
                  controller: _database,
                  autocorrect: false,
                  decoration: InputDecoration(
                    labelText: l.integDatabase,
                    border: const OutlineInputBorder(),
                    isDense: true,
                  ),
                ),
              ],
              const SizedBox(height: 14),
              Align(
                alignment: Alignment.centerRight,
                child: FilledButton(
                  key: const Key('integration-save'),
                  onPressed: c.saving ? null : _save,
                  child: Text(l.integSave),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

/// What happened to a saved change: the agent has to fetch it and report
/// back, so "saved" is not the same as "applied".
String? applyPhaseText(L l, ApplyPhase phase) => switch (phase) {
  ApplyPhase.sent => l.integApplySent,
  ApplyPhase.awaitingStatus => l.integApplyAwaiting,
  ApplyPhase.disabled => l.integRemoteOff,
  ApplyPhase.idle => null,
};
