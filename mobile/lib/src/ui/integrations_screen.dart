// Integrations, as the agents report them.
//
// The same inventory search the web's page is built on: the discovered
// service's body says whether the integration is collecting, so this screen
// reads a status rather than deriving one -- and cannot disagree with the web.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../discovery.dart';
import '../integration_settings.dart';
import '../roles.dart';
import '../sections.dart';
import '../session.dart';
import 'cloud_screen.dart';
import 'integration_config_sheet.dart';
import 'sections_screen.dart';
import 'severity.dart';

class IntegrationsBody extends StatelessWidget {
  const IntegrationsBody({
    super.key,
    required this.session,
    required this.sections,
    required this.active,
  });

  final SessionController session;
  final Sections sections;
  final bool active;

  @override
  Widget build(BuildContext context) {
    final c = sections.integrations;

    return SectionBody<IntegrationInstance>(
      session: session,
      controller: c,
      searchKey: 'integrations-search',
      searchHint: (l) => l.integrationsSearch,
      active: active,
      emptyTitle: (l) => l.integrationsEmpty,
      header: ListenableBuilder(
        listenable: c,
        builder: (context, _) {
          // The cloud link is above the list and shows whether or not
          // anything was discovered: a managed service has no agent to
          // discover, which is why it is configured rather than found.
          final cloudLink = _CloudLink(session: session, sections: sections);
          if (c.items.isEmpty) return cloudLink;
          final l = L.of(context);
          final n = c.counts;
          final theme = Theme.of(context);
          return Padding(
            padding: const EdgeInsets.only(bottom: 8, left: 4),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  l.integCounts(n.enabled, n.needsConfiguration, n.error),
                  style: theme.textTheme.bodySmall,
                ),
                Text(
                  l.integConfigureOnWeb,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
                cloudLink,
              ],
            ),
          );
        },
      ),
      card: (context, i) =>
          _InstanceCard(instance: i, session: session, sections: sections),
    );
  }
}

class _InstanceCard extends StatefulWidget {
  const _InstanceCard({
    required this.instance,
    required this.session,
    required this.sections,
  });

  final IntegrationInstance instance;
  final SessionController session;
  final Sections sections;

  @override
  State<_InstanceCard> createState() => _InstanceCardState();
}

class _InstanceCardState extends State<_InstanceCard> {
  IntegrationSettingsController? _settings;

  @override
  void dispose() {
    _settings?.dispose();
    super.dispose();
  }

  /// Made the first time somebody configures this host's integration,
  /// not before: a list of forty instances must not ask the server forty
  /// times for settings nobody is editing.
  IntegrationSettingsController _settingsOf() =>
      _settings ??= widget.sections.integrationSettings(widget.instance.hostId);

  Future<void> _configure() async {
    final saved = await editIntegration(
      context,
      session: widget.session,
      controller: _settingsOf(),
      integration: widget.instance.name,
      instance: widget.instance.service.instance ?? widget.instance.key,
      hostName: widget.instance.hostName,
    );
    if (saved && mounted) {
      // Reload the list so the status follows once the agent applies it.
      await widget.sections.integrations.refresh();
    }
  }

  @override
  Widget build(BuildContext context) {
    final instance = widget.instance;
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final integ = instance.service.integration;
    final (label, level) = switch (instance.status) {
      DiscoveredServiceIntegrationStatus.enabled => (
        l.integEnabled,
        SeverityLevel.good,
      ),
      DiscoveredServiceIntegrationStatus.needsConfiguration => (
        l.integNeedsConfig,
        SeverityLevel.warning,
      ),
      DiscoveredServiceIntegrationStatus.error => (
        l.integError,
        SeverityLevel.critical,
      ),
      _ => (l.integNotAvailable, SeverityLevel.unknown),
    };

    return Card(
      key: Key('integration-${instance.hostId}-${instance.key}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(
                    instance.name,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.titleSmall,
                  ),
                ),
                const SizedBox(width: 8),
                Tag(label: label, level: level),
              ],
            ),
            const SizedBox(height: 4),
            Text(instance.hostName, style: muted),
            // The invoked path when it differs from the matched executable:
            // redis-server and redis-check-rdb are the same binary, and the
            // key alone would name the wrong one.
            Text(
              instance.service.displayInstance ??
                  instance.service.instance ??
                  instance.key,
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: muted,
            ),
            if (instance.service.version != null)
              Text(instance.service.version!, style: muted),
            if (integ?.endpoint != null && integ!.endpoint!.isNotEmpty)
              Text(integ.endpoint!, style: muted),
            // The error is set on a partial collection too, so it is worth
            // showing even next to "collecting".
            if (integ?.error != null && integ!.error!.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(top: 4),
                child: Text(
                  integ.error!,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.error,
                  ),
                ),
              ),
            // Only where there is something to set and only for the role
            // the server would accept: docker and IIS are configured by
            // the agent, and a member's tap would answer 403.
            if (isConfigurable(instance.name) &&
                can(widget.session.me?.role, 'fleet.manage'))
              Align(
                alignment: Alignment.centerLeft,
                child: TextButton(
                  key: Key('integration-configure-${instance.key}'),
                  onPressed: _configure,
                  child: Text(l.integConfigure),
                ),
              ),
            if (_settings != null)
              ListenableBuilder(
                listenable: _settings!,
                builder: (context, _) {
                  final text = applyPhaseText(l, _settings!.phase);
                  if (text == null) return const SizedBox.shrink();
                  return Text(
                    // Saved is not applied: the agent has to fetch it.
                    text,
                    key: const Key('integration-apply'),
                    style: muted,
                  );
                },
              ),
          ],
        ),
      ),
    );
  }
}

/// The way into the cloud connections, where the web keeps it: on the
/// integrations page, because managed services are configured rather than
/// discovered.
class _CloudLink extends StatelessWidget {
  const _CloudLink({required this.session, required this.sections});

  final SessionController session;
  final Sections sections;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return Padding(
      padding: const EdgeInsets.only(top: 8),
      child: Card(
        margin: EdgeInsets.zero,
        child: ListTile(
          key: const Key('integrations-cloud'),
          leading: const Icon(Icons.cloud_outlined),
          title: Text(l.cloudOpen),
          subtitle: Text(
            l.cloudOpenHint,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
          trailing: const Icon(Icons.chevron_right),
          onTap: () => Navigator.of(context).push(
            MaterialPageRoute<void>(
              builder: (_) => CloudScreen(session: session, sections: sections),
            ),
          ),
        ),
      ),
    );
  }
}
