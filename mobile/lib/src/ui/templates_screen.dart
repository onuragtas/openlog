// Making an alert rule from a phone.
//
// Not the web's rule editor: that is a metric, an aggregation, a window, two
// thresholds and a channel list, and filling it in on a phone would be worse
// in every way. A template is the same rule with those choices already made
// by someone who knew the metric, so what is left is a number and a target --
// which is a form a thumb can finish.
//
// The rule is still the server's: it renders the template, it previews it,
// and what gets stored is exactly what was previewed.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../session.dart';
import '../templates.dart';
import 'failure_text.dart';
import 'severity.dart';
import 'sparkline.dart';

class TemplatesScreen extends StatefulWidget {
  const TemplatesScreen({
    super.key,
    required this.session,
    required this.sections,
  });

  final SessionController session;
  final Sections sections;

  @override
  State<TemplatesScreen> createState() => _TemplatesScreenState();
}

class _TemplatesScreenState extends State<TemplatesScreen> {
  @override
  void initState() {
    super.initState();
    final c = widget.sections.templates;
    if (!c.loaded && !c.loadingFirst) {
      WidgetsBinding.instance.addPostFrameCallback((_) => c.refresh());
    }
  }

  String _categoryLabel(L l, String key) => switch (key) {
    'host' => l.templatesHosts,
    'container' => l.templatesContainers,
    'apm' => l.templatesApm,
    'integration' => l.templatesIntegrations,
    'kubernetes' => l.templatesKubernetes,
    _ => l.templatesAll,
  };

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.sections.templates;
    final theme = Theme.of(context);

    return Scaffold(
      appBar: AppBar(
        title: Text(l.templatesTitle),
        actions: [
          IconButton(
            key: const Key('templates-refresh'),
            tooltip: l.refresh,
            onPressed: c.refresh,
            icon: const Icon(Icons.refresh),
          ),
        ],
      ),
      body: ListenableBuilder(
        listenable: c,
        builder: (context, _) => Column(
          children: [
            SingleChildScrollView(
              scrollDirection: Axis.horizontal,
              padding: const EdgeInsets.fromLTRB(12, 10, 12, 2),
              child: Row(
                children: [
                  for (final key in ['', ...templateCategories])
                    Padding(
                      padding: const EdgeInsets.only(right: 8),
                      child: ChoiceChip(
                        key: Key(
                          'templates-category-${key.isEmpty ? 'all' : key}',
                        ),
                        label: Text(_categoryLabel(l, key)),
                        selected: c.category == key,
                        onSelected: (_) {
                          if (c.category == key) return;
                          c.category = key;
                          c.refresh();
                        },
                      ),
                    ),
                ],
              ),
            ),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 16),
              child: FailureBanner(
                failure: c.failure,
                baseUrl: widget.session.baseUrl ?? '',
              ),
            ),
            if (c.unavailable.isNotEmpty)
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 4, 16, 0),
                child: Text(
                  // Named with the server's reason: "no rule of that type"
                  // is a thing to know before setting one up, and the reason
                  // is the server's to give.
                  l.templatesUnavailable(
                    [
                      for (final t in c.unavailable)
                        t.reason.isEmpty
                            ? t.type.wire
                            : '${t.type.wire} (${t.reason})',
                    ].join(', '),
                  ),
                  key: const Key('templates-unavailable'),
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              ),
            Expanded(
              child: c.loadingFirst
                  ? const Center(child: CircularProgressIndicator())
                  : c.items.isEmpty
                  ? Center(
                      child: Text(
                        l.templatesEmpty,
                        key: const Key('templates-empty'),
                        style: theme.textTheme.bodyMedium?.copyWith(
                          color: theme.colorScheme.onSurfaceVariant,
                        ),
                      ),
                    )
                  : RefreshIndicator(
                      onRefresh: c.refresh,
                      child: ListView.builder(
                        padding: const EdgeInsets.fromLTRB(12, 6, 12, 28),
                        itemCount: c.items.length,
                        itemBuilder: (context, i) => _TemplateCard(
                          template: c.items[i],
                          onOpen: () => Navigator.of(context).push(
                            MaterialPageRoute<void>(
                              builder: (_) => TemplateSetupScreen(
                                session: widget.session,
                                sections: widget.sections,
                                template: c.items[i],
                              ),
                            ),
                          ),
                        ),
                      ),
                    ),
            ),
          ],
        ),
      ),
    );
  }
}

class _TemplateCard extends StatelessWidget {
  const _TemplateCard({required this.template, required this.onOpen});

  final AlertTemplate template;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final locale = Localizations.localeOf(context).toString();

    return Card(
      key: Key('template-${template.id}'),
      margin: const EdgeInsets.symmetric(vertical: 5),
      child: ListTile(
        contentPadding: const EdgeInsets.fromLTRB(14, 6, 10, 6),
        title: Text(
          templateText(template.name, locale),
          style: theme.textTheme.titleSmall,
        ),
        subtitle: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const SizedBox(height: 4),
            Text(templateText(template.description, locale)),
            const SizedBox(height: 6),
            Wrap(
              spacing: 8,
              runSpacing: 4,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                SeverityChip(severity: template.severity),
                Text(
                  template.integration == null || template.integration!.isEmpty
                      ? template.category.wire
                      : template.integration!,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              ],
            ),
          ],
        ),
        trailing: Icon(Icons.chevron_right, semanticLabel: l.templatesSetUp),
        onTap: onOpen,
      ),
    );
  }
}

/// One template's values, what it would have done, and the button that makes
/// it real.
class TemplateSetupScreen extends StatefulWidget {
  const TemplateSetupScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.template,
  });

  final SessionController session;
  final Sections sections;
  final AlertTemplate template;

  @override
  State<TemplateSetupScreen> createState() => _TemplateSetupScreenState();
}

class _TemplateSetupScreenState extends State<TemplateSetupScreen> {
  late final List<AlertTemplateParam> _params;
  late final Map<String, String> _values;
  late final Map<String, TextEditingController> _fields;
  TemplateSetupController? _controller;

  @override
  void initState() {
    super.initState();
    _params = editableParams(widget.template);
    _values = initialValues(_params);
    _fields = {
      for (final p in _params)
        p.key: TextEditingController(text: _values[p.key]),
    };
  }

  @override
  void didChangeDependencies() {
    super.didChangeDependencies();
    // Not in initState: the locale comes from an inherited widget, and
    // reading one there throws. The controller needs it because the rule's
    // generated name and description come back in that language -- a rule
    // made from a Turkish phone reads as Turkish on the web too.
    _controller ??= widget.sections.templateSetup(
      widget.template,
      templateLanguage(Localizations.localeOf(context).toString()),
    );
  }

  @override
  void dispose() {
    for (final f in _fields.values) {
      f.dispose();
    }
    _controller?.dispose();
    super.dispose();
  }

  String? _errorText(L l, ParamError? e) => switch (e?.kind) {
    'required' => l.templatesRequired,
    'number' => l.templatesNumber,
    'range' => l.templatesRange(
      _plain(e!.min ?? double.negativeInfinity),
      _plain(e.max ?? double.infinity),
    ),
    _ => null,
  };

  String _plain(double v) =>
      v == v.roundToDouble() && v.abs() < 1e9 ? '${v.toInt()}' : '$v';

  String? _unitSuffix(L l, AlertTemplateParam p) => switch (p.unit) {
    'ratio' => '%',
    'seconds' => l.templatesSeconds,
    'ms' => 'ms',
    'per_second' => l.templatesPerSecond,
    _ => null,
  };

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final locale = Localizations.localeOf(context).toString();
    final built = buildParams(_params, _values);
    final valid = built.errors.isEmpty;
    final controller = _controller!;

    return Scaffold(
      appBar: AppBar(title: Text(templateText(widget.template.name, locale))),
      body: ListenableBuilder(
        listenable: controller,
        builder: (context, _) => ListView(
          padding: const EdgeInsets.fromLTRB(16, 14, 16, 28),
          children: [
            Text(
              templateText(widget.template.description, locale),
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
            FailureBanner(
              failure: controller.failure,
              baseUrl: widget.session.baseUrl ?? '',
            ),
            const SizedBox(height: 12),
            for (final p in _params) ...[
              TextField(
                key: Key('template-param-${p.key}'),
                controller: _fields[p.key],
                keyboardType:
                    p.kind == AlertTemplateParamKind.number ||
                        p.kind == AlertTemplateParamKind.duration
                    ? const TextInputType.numberWithOptions(decimal: true)
                    : TextInputType.text,
                onChanged: (v) => setState(() => _values[p.key] = v),
                decoration: InputDecoration(
                  labelText: () {
                    final label = templateText(p.label, locale);
                    final suffix = _unitSuffix(l, p);
                    return suffix == null ? label : '$label ($suffix)';
                  }(),
                  errorText: _errorText(l, built.errors[p.key]),
                  border: const OutlineInputBorder(),
                  isDense: true,
                ),
              ),
              const SizedBox(height: 10),
            ],
            Row(
              children: [
                Expanded(
                  child: OutlinedButton.icon(
                    key: const Key('template-check'),
                    onPressed: controller.busy || !valid
                        ? null
                        : () => controller.check(built.params),
                    icon: const Icon(Icons.visibility_outlined),
                    label: Text(l.templatesPreview),
                  ),
                ),
              ],
            ),
            if (controller.busy)
              const Padding(
                padding: EdgeInsets.symmetric(vertical: 20),
                child: Center(child: CircularProgressIndicator()),
              ),
            if (controller.rendered?.reference != null)
              Padding(
                padding: const EdgeInsets.only(top: 12),
                child: Text(
                  l.templatesReference(
                    controller.rendered!.reference!.metric,
                    _plain(controller.rendered!.reference!.value),
                  ),
                  key: const Key('template-reference'),
                  style: theme.textTheme.bodySmall,
                ),
              ),
            if (controller.preview != null)
              _Preview(preview: controller.preview!),
            if (controller.created != null)
              Padding(
                padding: const EdgeInsets.only(top: 14),
                child: Text(
                  l.templatesCreated(controller.created!),
                  key: const Key('template-created'),
                  style: theme.textTheme.bodyMedium?.copyWith(
                    color: severityTextColor(context, SeverityLevel.good),
                  ),
                ),
              )
            else if (controller.rendered != null)
              Padding(
                padding: const EdgeInsets.only(top: 14),
                child: FilledButton.icon(
                  key: const Key('template-create'),
                  // Creates the rule that was previewed, not a fresh render:
                  // what was shown is what gets stored.
                  onPressed: controller.busy ? null : controller.create,
                  icon: const Icon(Icons.add_alert_outlined),
                  label: Text(l.templatesCreate),
                ),
              ),
          ],
        ),
      ),
    );
  }
}

/// What the rule would have done over the last six hours.
class _Preview extends StatelessWidget {
  const _Preview({required this.preview});

  final AlertRulePreview preview;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final series = busiestSeries(preview);
    final points = <double>[
      if (series != null)
        for (final (_, value) in series.points) ?value,
    ];

    return Padding(
      padding: const EdgeInsets.only(top: 14),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            // The number is the point of the preview: a rule that would have
            // fired forty times in six hours is a rule nobody will read.
            preview.series.isEmpty
                ? l.templatesNoData
                : l.templatesWouldFire(previewIncidents(preview)),
            key: const Key('template-would-fire'),
            style: theme.textTheme.titleSmall,
          ),
          if (preview.threshold != null)
            Text(
              // In the unit the form used: the field says "Eşik (%)" and
              // takes 85, so a line underneath saying 0.85 reads as a
              // different threshold.
              l.templatesThreshold(
                preview.unit == 'ratio'
                    ? l.templatesPercent(_number(preview.threshold! * 100))
                    : _number(preview.threshold!),
              ),
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          if (series != null && points.length > 1) ...[
            const SizedBox(height: 8),
            SizedBox(
              height: 64,
              child: Sparkline(
                values: points,
                color: theme.colorScheme.primary,
              ),
            ),
            if (preview.series.length > 1)
              Padding(
                padding: const EdgeInsets.only(top: 4),
                child: Text(
                  // Says which line is drawn, because the number above counts
                  // every series and the line is only one of them.
                  l.templatesOneSeries(preview.series.length),
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              ),
          ],
        ],
      ),
    );
  }
}

/// Enough digits to be useful and not enough to be noise.
String _number(double v) {
  final abs = v.abs();
  final fixed = abs >= 100
      ? v.toStringAsFixed(0)
      : abs >= 1
      ? v.toStringAsFixed(2)
      : v.toStringAsFixed(4);
  if (!fixed.contains('.')) return fixed;
  return fixed
      .replaceFirst(RegExp(r'0+$'), '')
      .replaceFirst(RegExp(r'\.$'), '');
}
