// The alert rules, and the one write an on-call person makes from a phone:
// turning a noisy rule off.
//
// Everything else about a rule is a form with a threshold in it, and three in
// the morning on a phone is the worst place to fill one in. The screen says
// where that is done instead.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../session.dart';
import 'sections_screen.dart';
import 'severity.dart';

class RulesBody extends StatelessWidget {
  const RulesBody({
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
    final c = sections.rules;

    return SectionBody<AlertRule>(
      session: session,
      controller: c,
      searchKey: 'rules-search',
      searchHint: (l) => l.rulesSearch,
      active: active,
      emptyTitle: (l) => l.rulesEmpty,
      header: Padding(
        padding: const EdgeInsets.only(bottom: 8, left: 4),
        child: Text(
          L.of(context).ruleEditOnWeb,
          style: Theme.of(context).textTheme.bodySmall?.copyWith(
            color: Theme.of(context).colorScheme.onSurfaceVariant,
          ),
        ),
      ),
      card: (context, rule) => ListenableBuilder(
        listenable: c,
        builder: (context, _) => _RuleCard(
          rule: rule,
          busy: c.busy == rule.id,
          onToggle: () => _confirm(context, c, rule),
        ),
      ),
    );
  }

  /// Asks first, and says what else happens.
  ///
  /// Disabling a rule resolves its open incidents (reason `rule_disabled`),
  /// which is not what "stop paging me" sounds like. Someone silencing a
  /// noisy rule at night should not discover afterwards that the incident
  /// they were working closed under them.
  Future<void> _confirm(
    BuildContext context,
    AlertRulesController c,
    AlertRule rule,
  ) async {
    final l = L.of(context);
    final turningOff = rule.enabled;
    final open = rule.status.openIncidents;

    final ok = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text(turningOff ? l.ruleDisableTitle : l.ruleEnableTitle),
        content: Text(
          !turningOff
              ? rule.name
              : open > 0
              ? l.ruleDisableBody(open)
              : l.ruleDisableBodyNone,
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(false),
            child: Text(l.ruleCancel),
          ),
          FilledButton(
            key: const Key('rule-confirm'),
            onPressed: () => Navigator.of(context).pop(true),
            child: Text(
              turningOff ? l.ruleDisableConfirm : l.ruleEnableConfirm,
            ),
          ),
        ],
      ),
    );
    if (ok ?? false) await c.setEnabled(rule.id, enabled: !rule.enabled);
  }
}

class _RuleCard extends StatelessWidget {
  const _RuleCard({
    required this.rule,
    required this.busy,
    required this.onToggle,
  });

  final AlertRule rule;
  final bool busy;
  final VoidCallback onToggle;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final (stateLabel, level) = switch (rule.status.state) {
      AlertRuleStatusState.firing => (l.ruleFiring, SeverityLevel.critical),
      AlertRuleStatusState.pending => (l.rulePending, SeverityLevel.warning),
      AlertRuleStatusState.error => (l.ruleError, SeverityLevel.critical),
      AlertRuleStatusState.ok => (l.ruleOk, SeverityLevel.good),
      AlertRuleStatusState.disabled => (l.ruleDisabled, SeverityLevel.unknown),
      _ => (l.ruleOk, SeverityLevel.unknown),
    };

    return Card(
      key: Key('rule-${rule.id}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(14, 10, 6, 10),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    rule.name,
                    maxLines: 2,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.titleSmall,
                  ),
                  const SizedBox(height: 6),
                  Wrap(
                    spacing: 8,
                    runSpacing: 4,
                    crossAxisAlignment: WrapCrossAlignment.center,
                    children: [
                      Tag(label: stateLabel, level: level),
                      SeverityChip(severity: rule.severity),
                      if (rule.status.openIncidents > 0)
                        Text(
                          l.ruleOpenIncidents(rule.status.openIncidents),
                          style: theme.textTheme.bodySmall?.copyWith(
                            color: severityTextColor(
                              context,
                              SeverityLevel.critical,
                            ),
                          ),
                        ),
                      Text(l.ruleEvery(rule.intervalSeconds), style: muted),
                    ],
                  ),
                  // An evaluation that is erroring is a rule that is not
                  // protecting anything, however healthy it looks.
                  if (rule.status.lastError.isNotEmpty)
                    Padding(
                      padding: const EdgeInsets.only(top: 4),
                      child: Text(
                        rule.status.lastError,
                        style: theme.textTheme.bodySmall?.copyWith(
                          color: theme.colorScheme.error,
                        ),
                      ),
                    ),
                ],
              ),
            ),
            busy
                ? const Padding(
                    padding: EdgeInsets.all(14),
                    child: SizedBox(
                      width: 18,
                      height: 18,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    ),
                  )
                : Switch(
                    key: Key('rule-switch-${rule.id}'),
                    value: rule.enabled,
                    onChanged: (_) => onToggle(),
                  ),
          ],
        ),
      ),
    );
  }
}
