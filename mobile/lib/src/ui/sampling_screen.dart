// Which traces are kept, and which are thrown away.
//
// Rules are tried in order and the first match decides, so the list is shown
// in that order and dragged to change it -- the same shape as alert routing,
// for the same reason.
//
// Nothing is sent until the save button. A policy applied halfway is a policy
// nobody chose: turning the rate limit down before adding the rule that keeps
// the errors would throw away the errors in between.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../sampling.dart';
import '../session.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'severity.dart';

class SamplingScreen extends StatefulWidget {
  const SamplingScreen({
    super.key,
    required this.session,
    required this.sampling,
  });

  final SessionController session;
  final SamplingController sampling;

  @override
  State<SamplingScreen> createState() => _SamplingScreenState();
}

class _SamplingScreenState extends State<SamplingScreen> {
  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => widget.sampling.load());
  }

  @override
  void dispose() {
    widget.sampling.dispose();
    super.dispose();
  }

  Future<void> _editRule(int? index) async {
    final c = widget.sampling;
    final draft = c.draft;
    if (draft == null) return;
    final rule = await showModalBottomSheet<TailSamplingRule>(
      context: context,
      isScrollControlled: true,
      builder: (context) => Padding(
        padding: EdgeInsets.only(
          bottom: MediaQuery.of(context).viewInsets.bottom,
        ),
        child: _RuleSheet(rule: index == null ? null : draft.rules[index]),
      ),
    );
    if (rule == null) return;
    final rules = [...draft.rules];
    if (index == null) {
      rules.add(rule);
    } else {
      rules[index] = rule;
    }
    c.edit(withRules(draft, rules));
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.sampling;

    return Scaffold(
      appBar: AppBar(title: Text(l.samplingTitle)),
      body: ListenableBuilder(
        listenable: c,
        builder: (context, _) {
          if (c.loading) {
            return const Center(child: CircularProgressIndicator());
          }
          final draft = c.draft;
          final state = c.state;
          if (draft == null || state == null) {
            return Padding(
              padding: const EdgeInsets.all(16),
              child: FailureBanner(
                failure: c.failure,
                baseUrl: widget.session.baseUrl ?? '',
              ),
            );
          }
          return ListView(
            padding: const EdgeInsets.fromLTRB(16, 12, 16, 28),
            children: [
              // Two different switches, and conflating them would be a lie:
              // this api may have the sampler turned off entirely, in which
              // case the stored policy is kept but nothing acts on it.
              if (!state.enabled)
                Padding(
                  padding: const EdgeInsets.only(bottom: 8),
                  child: Text(
                    l.samplingOffHere,
                    key: const Key('sampling-off-here'),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: severityTextColor(context, SeverityLevel.warning),
                    ),
                  ),
                ),
              if (state.isDefault)
                Padding(
                  padding: const EdgeInsets.only(bottom: 8),
                  child: Text(
                    l.samplingDefault,
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                )
              else if (state.updatedAt != null)
                Padding(
                  padding: const EdgeInsets.only(bottom: 8),
                  child: Text(
                    l.samplingUpdated(
                      relativeTimeOf(l, state.updatedAt!),
                      state.updatedByEmail,
                    ),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                ),
              FailureBanner(
                failure: c.failure,
                baseUrl: widget.session.baseUrl ?? '',
              ),
              SwitchListTile(
                key: const Key('sampling-enabled'),
                contentPadding: EdgeInsets.zero,
                title: Text(l.samplingEnabled),
                subtitle: Text(
                  l.samplingEnabledHint,
                  style: theme.textTheme.bodySmall,
                ),
                value: draft.enabled,
                onChanged: (v) => c.edit(
                  TailSamplingPolicy(
                    enabled: v,
                    baselineRatio: draft.baselineRatio,
                    maxSpansPerSecond: draft.maxSpansPerSecond,
                    rules: draft.rules,
                  ),
                ),
              ),
              _PercentField(
                fieldKey: const Key('sampling-baseline'),
                label: l.samplingBaseline,
                hint: l.samplingBaselineHint,
                value: draft.baselineRatio,
                onChanged: (v) => c.edit(
                  TailSamplingPolicy(
                    enabled: draft.enabled,
                    baselineRatio: v,
                    maxSpansPerSecond: draft.maxSpansPerSecond,
                    rules: draft.rules,
                  ),
                ),
              ),
              const SizedBox(height: 10),
              _NumberField(
                fieldKey: const Key('sampling-max-spans'),
                label: l.samplingMaxSpans,
                hint: l.samplingMaxSpansHint,
                value: draft.maxSpansPerSecond,
                onChanged: (v) => c.edit(
                  TailSamplingPolicy(
                    enabled: draft.enabled,
                    baselineRatio: draft.baselineRatio,
                    maxSpansPerSecond: v,
                    rules: draft.rules,
                  ),
                ),
              ),
              const SizedBox(height: 16),
              Row(
                children: [
                  Expanded(
                    child: Text(
                      l.samplingRules,
                      style: theme.textTheme.titleSmall,
                    ),
                  ),
                  TextButton.icon(
                    key: const Key('sampling-add-rule'),
                    onPressed: () => _editRule(null),
                    icon: const Icon(Icons.add),
                    label: Text(l.samplingAddRule),
                  ),
                ],
              ),
              Text(
                l.samplingOrder,
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
              if (draft.rules.isEmpty)
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 14),
                  child: Text(
                    l.samplingNoRules,
                    key: const Key('sampling-no-rules'),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                )
              else
                ReorderableListView.builder(
                  shrinkWrap: true,
                  physics: const NeverScrollableScrollPhysics(),
                  itemCount: draft.rules.length,
                  onReorder: (from, to) => c.edit(
                    withRules(draft, movedRules(draft.rules, from, to)),
                  ),
                  itemBuilder: (context, i) {
                    final rule = draft.rules[i];
                    return _RuleCard(
                      key: ValueKey('${rule.name}-$i'),
                      rule: rule,
                      index: i,
                      matched: _matchedOf(c.preview, rule.name),
                      onEdit: () => _editRule(i),
                      onDelete: () => c.edit(
                        withRules(draft, [...draft.rules]..removeAt(i)),
                      ),
                    );
                  },
                ),
              const SizedBox(height: 14),
              if (c.preview != null) _Preview(preview: c.preview!),
              const SizedBox(height: 10),
              Row(
                children: [
                  Expanded(
                    child: OutlinedButton.icon(
                      key: const Key('sampling-estimate'),
                      onPressed: c.busy ? null : () => c.estimate(),
                      icon: const Icon(Icons.calculate_outlined),
                      label: Text(l.samplingEstimate),
                    ),
                  ),
                  const SizedBox(width: 10),
                  Expanded(
                    child: FilledButton(
                      key: const Key('sampling-save'),
                      // Nothing to save is not the same as nothing to do:
                      // the button stays, disabled, so the screen does not
                      // rearrange itself while somebody edits.
                      onPressed: c.busy || !c.dirty ? null : c.save,
                      child: Text(l.samplingSave),
                    ),
                  ),
                ],
              ),
            ],
          );
        },
      ),
    );
  }

  double? _matchedOf(TailSamplingPreview? preview, String name) {
    for (final r in preview?.rules ?? const <TailSamplingPreviewRulesItem>[]) {
      if (r.name == name) return r.matchedTraceRatio;
    }
    return null;
  }
}

/// A ratio, edited as a percentage.
class _PercentField extends StatefulWidget {
  const _PercentField({
    required this.fieldKey,
    required this.label,
    required this.hint,
    required this.value,
    required this.onChanged,
  });

  final Key fieldKey;
  final String label;
  final String hint;
  final double value;
  final ValueChanged<double> onChanged;

  @override
  State<_PercentField> createState() => _PercentFieldState();
}

class _PercentFieldState extends State<_PercentField> {
  late final TextEditingController _text = TextEditingController(
    text: _show(widget.value * 100),
  );

  @override
  void dispose() {
    _text.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final parsed = double.tryParse(_text.text.trim().replaceAll(',', '.'));
    final bad = parsed == null || parsed < 0 || parsed > 100;
    return TextField(
      key: widget.fieldKey,
      controller: _text,
      keyboardType: const TextInputType.numberWithOptions(decimal: true),
      onChanged: (v) {
        setState(() {});
        final n = double.tryParse(v.trim().replaceAll(',', '.'));
        if (n != null && n >= 0 && n <= 100) widget.onChanged(n / 100);
      },
      decoration: InputDecoration(
        labelText: '${widget.label} (%)',
        helperText: widget.hint,
        helperMaxLines: 3,
        errorText: _text.text.trim().isEmpty || bad
            ? l.samplingPercentRange
            : null,
        border: const OutlineInputBorder(),
        isDense: true,
      ),
    );
  }
}

class _NumberField extends StatefulWidget {
  const _NumberField({
    required this.fieldKey,
    required this.label,
    required this.hint,
    required this.value,
    required this.onChanged,
  });

  final Key fieldKey;
  final String label;
  final String hint;
  final double value;
  final ValueChanged<double> onChanged;

  @override
  State<_NumberField> createState() => _NumberFieldState();
}

class _NumberFieldState extends State<_NumberField> {
  late final TextEditingController _text = TextEditingController(
    text: _show(widget.value),
  );

  @override
  void dispose() {
    _text.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) => TextField(
    key: widget.fieldKey,
    controller: _text,
    keyboardType: const TextInputType.numberWithOptions(decimal: true),
    onChanged: (v) {
      final n = double.tryParse(v.trim().replaceAll(',', '.'));
      if (n != null && n >= 0) widget.onChanged(n);
    },
    decoration: InputDecoration(
      labelText: widget.label,
      helperText: widget.hint,
      helperMaxLines: 3,
      border: const OutlineInputBorder(),
      isDense: true,
    ),
  );
}

class _RuleCard extends StatelessWidget {
  const _RuleCard({
    super.key,
    required this.rule,
    required this.index,
    required this.matched,
    required this.onEdit,
    required this.onDelete,
  });

  final TailSamplingRule rule;
  final int index;

  /// What the last estimate said this rule matched, as a ratio of traces.
  final double? matched;
  final VoidCallback onEdit;
  final VoidCallback onDelete;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return Card(
      margin: const EdgeInsets.symmetric(vertical: 4),
      child: Padding(
        padding: const EdgeInsets.fromLTRB(12, 8, 4, 8),
        child: Row(
          children: [
            Padding(
              padding: const EdgeInsets.only(right: 10),
              child: Text(
                '${index + 1}',
                style: theme.textTheme.titleMedium?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            ),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(rule.name, style: theme.textTheme.titleSmall),
                  Text(describeSamplingRule(l, rule), style: muted),
                  if (matched != null)
                    Text(l.samplingMatched(_percent(matched!)), style: muted),
                ],
              ),
            ),
            IconButton(
              key: Key('sampling-edit-$index'),
              tooltip: l.calendarsEdit,
              onPressed: onEdit,
              icon: const Icon(Icons.edit_outlined, size: 20),
            ),
            IconButton(
              key: Key('sampling-delete-$index'),
              tooltip: l.calendarsDelete,
              onPressed: onDelete,
              icon: const Icon(Icons.delete_outline, size: 20),
            ),
            ReorderableDragStartListener(
              index: index,
              child: Padding(
                padding: const EdgeInsets.symmetric(horizontal: 4),
                child: Icon(
                  Icons.drag_handle,
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _Preview extends StatelessWidget {
  const _Preview({required this.preview});

  final TailSamplingPreview preview;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          l.samplingKeeps(
            _percent(preview.keptTraceRatio),
            _percent(preview.keptSpanRatio),
          ),
          key: const Key('sampling-keeps'),
          style: theme.textTheme.titleSmall,
        ),
        Text(
          l.samplingExamined(preview.tracesExamined, preview.windowMinutes),
          style: theme.textTheme.bodySmall?.copyWith(
            color: theme.colorScheme.onSurfaceVariant,
          ),
        ),
        // The rate limit is not simulated, so a policy that looks like it
        // keeps everything may still drop spans once the sampler is busy.
        Text(
          l.samplingNoRateLimit,
          style: theme.textTheme.bodySmall?.copyWith(
            color: theme.colorScheme.onSurfaceVariant,
          ),
        ),
      ],
    );
  }
}

/// The sheet that makes or edits one rule.
class _RuleSheet extends StatefulWidget {
  const _RuleSheet({required this.rule});

  final TailSamplingRule? rule;

  @override
  State<_RuleSheet> createState() => _RuleSheetState();
}

class _RuleSheetState extends State<_RuleSheet> {
  late TailSamplingRuleType _type =
      widget.rule?.type ?? TailSamplingRuleType.error;
  late final _name = TextEditingController(text: widget.rule?.name ?? '');
  late final _ratio = TextEditingController(
    text: _show((widget.rule?.ratio ?? 1) * 100),
  );
  late final _threshold = TextEditingController(
    text: widget.rule?.thresholdMs?.toString() ?? '',
  );
  late final _service = TextEditingController(text: widget.rule?.service ?? '');
  late final _services = TextEditingController(
    text: (widget.rule?.services ?? const []).join(', '),
  );
  late final _route = TextEditingController(text: widget.rule?.route ?? '');
  late final _key = TextEditingController(text: widget.rule?.key ?? '');
  late final _value = TextEditingController(text: widget.rule?.value ?? '');

  @override
  void dispose() {
    for (final c in [
      _name,
      _ratio,
      _threshold,
      _service,
      _services,
      _route,
      _key,
      _value,
    ]) {
      c.dispose();
    }
    super.dispose();
  }

  String _label(L l, TailSamplingRuleType t) => switch (t) {
    TailSamplingRuleType.error => l.samplingTypeError,
    TailSamplingRuleType.latency => l.samplingTypeLatency,
    TailSamplingRuleType.service => l.samplingTypeService,
    TailSamplingRuleType.route => l.samplingTypeRoute,
    TailSamplingRuleType.attribute => l.samplingTypeAttribute,
    TailSamplingRuleType.unknown => t.wire,
  };

  /// What the server needs for this type, and nothing else: a `route` on an
  /// error rule is a field the contract refuses (`additionalProperties:
  /// false`), so the sheet sends only the fields of the type chosen.
  TailSamplingRule? _build() {
    final name = _name.text.trim();
    if (name.isEmpty) return null;
    final ratio = double.tryParse(_ratio.text.trim().replaceAll(',', '.'));
    if (ratio == null || ratio < 0 || ratio > 100) return null;
    final threshold = int.tryParse(_threshold.text.trim());
    final services = [
      for (final s in _services.text.split(RegExp(r'[,\s]+')))
        if (s.trim().isNotEmpty) s.trim(),
    ];
    final service = _service.text.trim();
    switch (_type) {
      case TailSamplingRuleType.latency:
        if (threshold == null || threshold < 1) return null;
      case TailSamplingRuleType.service:
        if (services.isEmpty) return null;
      case TailSamplingRuleType.route:
        if (_route.text.trim().isEmpty) return null;
      case TailSamplingRuleType.attribute:
        if (_key.text.trim().isEmpty) return null;
      case TailSamplingRuleType.error:
      case TailSamplingRuleType.unknown:
        break;
    }
    return TailSamplingRule(
      name: name,
      type: _type,
      ratio: ratio / 100,
      thresholdMs: _type == TailSamplingRuleType.latency ? threshold : null,
      service: service.isNotEmpty && _type != TailSamplingRuleType.service
          ? service
          : null,
      services: _type == TailSamplingRuleType.service ? services : null,
      route: _type == TailSamplingRuleType.route ? _route.text.trim() : null,
      key: _type == TailSamplingRuleType.attribute ? _key.text.trim() : null,
      value: _type == TailSamplingRuleType.attribute
          ? _value.text.trim()
          : null,
    );
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final built = _build();

    return SafeArea(
      child: SingleChildScrollView(
        padding: const EdgeInsets.fromLTRB(16, 16, 16, 24),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              widget.rule == null ? l.samplingAddRule : l.samplingEditRule,
              style: Theme.of(context).textTheme.titleMedium,
            ),
            const SizedBox(height: 12),
            TextField(
              key: const Key('rule-name'),
              controller: _name,
              onChanged: (_) => setState(() {}),
              decoration: InputDecoration(
                labelText: l.samplingRuleName,
                border: const OutlineInputBorder(),
                isDense: true,
              ),
            ),
            const SizedBox(height: 10),
            DropdownButtonFormField<TailSamplingRuleType>(
              key: const Key('rule-type'),
              initialValue: _type,
              decoration: InputDecoration(
                labelText: l.samplingRuleType,
                border: const OutlineInputBorder(),
                isDense: true,
              ),
              items: [
                for (final t in samplingRuleTypes)
                  DropdownMenuItem(value: t, child: Text(_label(l, t))),
              ],
              onChanged: (t) => setState(() => _type = t ?? _type),
            ),
            const SizedBox(height: 10),
            TextField(
              key: const Key('rule-ratio'),
              controller: _ratio,
              keyboardType: const TextInputType.numberWithOptions(
                decimal: true,
              ),
              onChanged: (_) => setState(() {}),
              decoration: InputDecoration(
                labelText: '${l.samplingRuleRatio} (%)',
                border: const OutlineInputBorder(),
                isDense: true,
              ),
            ),
            if (_type == TailSamplingRuleType.latency) ...[
              const SizedBox(height: 10),
              TextField(
                key: const Key('rule-threshold'),
                controller: _threshold,
                keyboardType: TextInputType.number,
                onChanged: (_) => setState(() {}),
                decoration: InputDecoration(
                  labelText: l.samplingRuleThreshold,
                  border: const OutlineInputBorder(),
                  isDense: true,
                ),
              ),
            ],
            if (_type == TailSamplingRuleType.service) ...[
              const SizedBox(height: 10),
              TextField(
                key: const Key('rule-services'),
                controller: _services,
                onChanged: (_) => setState(() {}),
                decoration: InputDecoration(
                  labelText: l.samplingRuleServices,
                  helperText: l.samplingRuleServicesHint,
                  border: const OutlineInputBorder(),
                  isDense: true,
                ),
              ),
            ] else ...[
              const SizedBox(height: 10),
              TextField(
                key: const Key('rule-service'),
                controller: _service,
                onChanged: (_) => setState(() {}),
                decoration: InputDecoration(
                  labelText: l.samplingRuleService,
                  helperText: l.samplingRuleServiceHint,
                  helperMaxLines: 2,
                  border: const OutlineInputBorder(),
                  isDense: true,
                ),
              ),
            ],
            if (_type == TailSamplingRuleType.route) ...[
              const SizedBox(height: 10),
              TextField(
                key: const Key('rule-route'),
                controller: _route,
                onChanged: (_) => setState(() {}),
                decoration: InputDecoration(
                  labelText: l.samplingRuleRoute,
                  helperText: l.samplingRuleRouteHint,
                  helperMaxLines: 2,
                  border: const OutlineInputBorder(),
                  isDense: true,
                ),
              ),
            ],
            if (_type == TailSamplingRuleType.attribute) ...[
              const SizedBox(height: 10),
              TextField(
                key: const Key('rule-key'),
                controller: _key,
                onChanged: (_) => setState(() {}),
                decoration: InputDecoration(
                  labelText: l.samplingRuleKey,
                  border: const OutlineInputBorder(),
                  isDense: true,
                ),
              ),
              const SizedBox(height: 10),
              TextField(
                key: const Key('rule-value'),
                controller: _value,
                onChanged: (_) => setState(() {}),
                decoration: InputDecoration(
                  labelText: l.samplingRuleValue,
                  helperText: l.samplingRuleValueHint,
                  helperMaxLines: 2,
                  border: const OutlineInputBorder(),
                  isDense: true,
                ),
              ),
            ],
            const SizedBox(height: 14),
            Row(
              mainAxisAlignment: MainAxisAlignment.end,
              children: [
                TextButton(
                  onPressed: () => Navigator.of(context).pop(),
                  child: Text(l.ruleCancel),
                ),
                const SizedBox(width: 8),
                FilledButton(
                  key: const Key('rule-save'),
                  onPressed: built == null
                      ? null
                      : () => Navigator.of(context).pop(built),
                  child: Text(l.calendarsSave),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}

/// One rule in a line: what it matches and how much of it it keeps.
String describeSamplingRule(L l, TailSamplingRule rule) {
  final what = switch (rule.type) {
    TailSamplingRuleType.error => l.samplingTypeError,
    TailSamplingRuleType.latency => l.samplingOverMs(rule.thresholdMs ?? 0),
    TailSamplingRuleType.service => (rule.services ?? const []).join(', '),
    TailSamplingRuleType.route => rule.route ?? '',
    TailSamplingRuleType.attribute =>
      (rule.value ?? '').isEmpty ? '${rule.key}' : '${rule.key}=${rule.value}',
    TailSamplingRuleType.unknown => rule.type.wire,
  };
  final scope = (rule.service ?? '').isEmpty ? '' : ' · ${rule.service}';
  return '$what$scope · ${l.samplingKeepRatio(_percent(rule.ratio ?? 1))}';
}

/// A ratio as a percentage, without digits it does not mean.
String _percent(double ratio) => _show(ratio * 100);

String _show(double v) {
  if (v == v.roundToDouble()) return '${v.toInt()}';
  final fixed = v.toStringAsFixed(v.abs() < 1 ? 3 : 1);
  return fixed
      .replaceFirst(RegExp(r'0+$'), '')
      .replaceFirst(RegExp(r'\.$'), '');
}
