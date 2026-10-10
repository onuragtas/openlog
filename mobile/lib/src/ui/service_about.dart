// Where a service runs, and what its Apdex is judged against.
//
// The web puts this above the tabs: language, version, hosts, containers,
// pods and the Apdex setting. On a phone that strip would push the tabs off
// the first screen, so it is the first thing inside the overview instead --
// same content, one scroll lower.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../services.dart';
import '../session.dart';
import 'container_screen.dart';
import 'failure_text.dart';
import 'host_screen.dart';
import 'pod_screen.dart';
import 'severity.dart';

class ServiceAbout extends StatelessWidget {
  const ServiceAbout({
    super.key,
    required this.session,
    required this.sections,
    required this.controller,
  });

  final SessionController session;
  final Sections sections;
  final ServiceAboutController controller;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return ListenableBuilder(
      listenable: controller,
      builder: (context, _) {
        final detail = controller.detail;
        final instance = detail == null || detail.instances.isEmpty
            ? null
            : detail.instances.first;
        final environments = {
          for (final i in detail?.instances ?? const <ApmServiceInstance>[])
            if (i.environment.isNotEmpty) i.environment,
        };

        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            FailureBanner(
              failure: controller.failure,
              baseUrl: session.baseUrl ?? '',
            ),
            Wrap(
              spacing: 8,
              runSpacing: 4,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                if (instance != null && instance.language.isNotEmpty)
                  Tag(label: instance.language, level: SeverityLevel.unknown),
                if (instance != null && instance.version.isNotEmpty)
                  Text(
                    'v${instance.version}',
                    style: theme.textTheme.bodySmall,
                  ),
                for (final env in environments)
                  Text(
                    env,
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                _Apdex(controller: controller),
              ],
            ),
            if (detail != null && detail.hosts.isNotEmpty)
              _Chips(
                label: l.serviceHosts,
                children: [
                  for (final h in detail.hosts)
                    _Chip(
                      key: Key('service-host-${h.hostId}'),
                      label: h.hostName.isEmpty ? h.hostId : h.hostName,
                      // A host the infra agent never reported has no page to
                      // open; the web dashes its border for the same reason.
                      onTap: !h.known
                          ? null
                          : () => Navigator.of(context).push(
                              MaterialPageRoute<void>(
                                builder: (_) => HostScreen(
                                  session: session,
                                  sections: sections,
                                  hostId: h.hostId,
                                  hostName: h.hostName.isEmpty
                                      ? h.hostId
                                      : h.hostName,
                                ),
                              ),
                            ),
                    ),
                ],
              ),
            if (controller.containers.isNotEmpty)
              _Chips(
                label: l.serviceContainers,
                children: [
                  for (final c in controller.containers)
                    _Chip(
                      key: Key('service-container-${c.containerId}'),
                      label: c.name.isEmpty ? c.containerId : c.name,
                      onTap: !c.known
                          ? null
                          : () => Navigator.of(context).push(
                              MaterialPageRoute<void>(
                                builder: (_) => ContainerScreen(
                                  session: session,
                                  sections: sections,
                                  containerId: c.containerId,
                                  name: c.name.isEmpty ? c.containerId : c.name,
                                ),
                              ),
                            ),
                    ),
                ],
              ),
            if (controller.pods.isNotEmpty)
              _Chips(
                label: l.servicePods,
                children: [
                  for (final p in controller.pods)
                    _Chip(
                      key: Key('service-pod-${p.podUid}'),
                      label: p.podName,
                      onTap: () => Navigator.of(context).push(
                        MaterialPageRoute<void>(
                          builder: (_) => PodScreen(
                            session: session,
                            sections: sections,
                            podUid: p.podUid,
                            podName: p.podName,
                          ),
                        ),
                      ),
                    ),
                ],
              ),
          ],
        );
      },
    );
  }
}

/// The Apdex threshold, and the one way to change it.
class _Apdex extends StatelessWidget {
  const _Apdex({required this.controller});

  final ServiceAboutController controller;

  Future<void> _edit(BuildContext context) async {
    final l = L.of(context);
    final settings = controller.settings;
    final text = TextEditingController(
      text: '${(settings?.apdexTMs ?? 500).round()}',
    );
    final value = await showDialog<int>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text(l.apdexTitle),
        content: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(l.apdexExplain, style: Theme.of(context).textTheme.bodySmall),
            const SizedBox(height: 10),
            TextField(
              key: const Key('apdex-value'),
              controller: text,
              autofocus: true,
              keyboardType: TextInputType.number,
              decoration: InputDecoration(
                labelText: l.apdexThreshold,
                border: const OutlineInputBorder(),
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
            key: const Key('apdex-save'),
            onPressed: () {
              final n = int.tryParse(text.text.trim());
              // 1..600000 is the contract's range; anything else is a 400,
              // and the dialog is the place to find that out.
              if (n == null || n < 1 || n > 600000) return;
              Navigator.of(context).pop(n);
            },
            child: Text(l.calendarsSave),
          ),
        ],
      ),
    );
    text.dispose();
    if (value != null) await controller.setApdex(value);
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final settings = controller.settings;
    if (settings == null) return const SizedBox.shrink();

    return InkWell(
      key: const Key('service-apdex'),
      onTap: controller.saving ? null : () => _edit(context),
      borderRadius: BorderRadius.circular(8),
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 4, vertical: 2),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(
              // Saying it is the default matters: changing it here makes it
              // this service's own, which is a different thing from the
              // organization's number happening to be the same.
              settings.isDefault
                  ? l.apdexDefault(settings.apdexTMs.round())
                  : l.apdexSet(settings.apdexTMs.round()),
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
            const SizedBox(width: 2),
            Icon(
              Icons.edit_outlined,
              size: 14,
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ],
        ),
      ),
    );
  }
}

class _Chips extends StatelessWidget {
  const _Chips({required this.label, required this.children});

  final String label;
  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.only(top: 6),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            label,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
          const SizedBox(height: 2),
          Wrap(spacing: 6, runSpacing: 6, children: children),
        ],
      ),
    );
  }
}

class _Chip extends StatelessWidget {
  const _Chip({super.key, required this.label, required this.onTap});

  final String label;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final known = onTap != null;
    final chip = InkWell(
      onTap: onTap,
      borderRadius: BorderRadius.circular(8),
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
        decoration: BoxDecoration(
          borderRadius: BorderRadius.circular(8),
          border: Border.all(color: theme.colorScheme.outlineVariant),
          // A name that goes nowhere is filled in rather than outlined: the
          // web dashes its border, which Flutter does not draw, and two
          // chips that differ only by text colour do not differ at all.
          color: known ? null : theme.colorScheme.surfaceContainerHighest,
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Text(
              label,
              style: theme.textTheme.bodySmall?.copyWith(
                color: known
                    ? theme.colorScheme.onSurface
                    : theme.colorScheme.onSurfaceVariant,
              ),
            ),
            if (known) ...[
              const SizedBox(width: 2),
              Icon(
                Icons.chevron_right,
                size: 14,
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ],
          ],
        ),
      ),
    );
    // Says why it goes nowhere, which is the whole difference: this host has
    // no infra agent data, so there is no page of it to open.
    return known ? chip : Tooltip(message: l.serviceUnknownHost, child: chip);
  }
}
