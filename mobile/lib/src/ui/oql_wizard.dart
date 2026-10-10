// The guided query builder: pick what to look at, get a query.
//
// The web's wizard sits above the editor with its rows of selects; on a
// phone the same picks are rows of buttons, and a list that can be hundreds
// of attributes long opens as a searchable sheet rather than a dropdown that
// would reach past the screen. Same questions, same order, same OQL out:
// `oql_builder.dart` writes the text for both.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../fields.dart';
import '../oql.dart';
import '../oql_builder.dart';
import '../session.dart';
import 'failure_text.dart';

/// Which explorer signal an event type's values come from. Hosts and
/// containers have none, so those rows have no suggestions -- the web's
/// wizard does the same rather than offering an empty list.
const _signalOf = {
  'Log': 'logs',
  'Span': 'traces',
  'Transaction': 'traces',
  'Metric': 'metrics',
};

class OqlWizard extends StatefulWidget {
  const OqlWizard({
    super.key,
    required this.session,
    required this.schema,
    required this.fields,
    required this.onRun,
  });

  final SessionController session;
  final OqlSchemaController schema;

  /// The dictionary of one signal, for value suggestions. Made once per
  /// wizard rather than per row: it holds which key is being looked at.
  final FieldsController Function(String signal) fields;

  /// Puts the query in the console's box and runs it.
  final void Function(String query) onRun;

  @override
  State<OqlWizard> createState() => _OqlWizardState();
}

class _OqlWizardState extends State<OqlWizard> {
  var _state = BuilderState();
  final _fields = <String, FieldsController>{};

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback(
      (_) => widget.schema.load(eventType: _state.eventType),
    );
  }

  @override
  void dispose() {
    for (final c in _fields.values) {
      c.dispose();
    }
    super.dispose();
  }

  FieldsController _fieldsOf(String signal) =>
      _fields.putIfAbsent(signal, () => widget.fields(signal));

  void _setEventType(String name) {
    setState(() {
      _state = BuilderState(eventType: name, timeseries: _state.timeseries);
    });
    widget.schema.load(eventType: name);
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final schema = widget.schema;

    return ListenableBuilder(
      listenable: schema,
      builder: (context, _) {
        final keys = schema.keysOf(_state.eventType);
        final measureKeys = numberMeasures.contains(_state.measure)
            ? schema.numberKeysOf(_state.eventType)
            : keys;
        final query = buildOql(_state);
        final issue = builderIssue(_state);

        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              l.wizardSubtitle,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
            FailureBanner(
              failure: schema.failure,
              baseUrl: widget.session.baseUrl ?? '',
            ),
            const SizedBox(height: 10),
            _Dropdown(
              fieldKey: const Key('wizard-event-type'),
              label: l.wizardDataType,
              value: _state.eventType,
              items: {for (final e in oqlEventTypes) e: _eventTypeLabel(l, e)},
              onChanged: _setEventType,
            ),
            const SizedBox(height: 10),
            _Dropdown(
              fieldKey: const Key('wizard-measure'),
              label: l.wizardMeasure,
              value: _state.measure,
              items: {for (final m in builderMeasures) m: _measureLabel(l, m)},
              onChanged: (v) => setState(() {
                _state.measure = v;
                // A number measure cannot be taken of a text attribute, so
                // one picked for `latest` does not survive the switch.
                if (numberMeasures.contains(v) &&
                    !schema
                        .numberKeysOf(_state.eventType)
                        .contains(_state.attribute)) {
                  _state.attribute = _state.eventType == 'Metric'
                      ? 'value'
                      : '';
                }
              }),
            ),
            if (_state.measure != 'count') ...[
              const SizedBox(height: 10),
              _PickRow(
                rowKey: const Key('wizard-attribute'),
                label: l.wizardAttribute,
                value: _state.attribute,
                empty: l.wizardChooseAttribute,
                onTap: () async {
                  final picked = await pickOne(
                    context,
                    title: l.wizardAttribute,
                    options: measureKeys,
                  );
                  if (picked != null) {
                    setState(() => _state.attribute = picked);
                  }
                },
              ),
            ],
            if (_state.eventType == 'Metric') ...[
              const SizedBox(height: 10),
              _PickRow(
                rowKey: const Key('wizard-metric'),
                label: l.wizardMetric,
                value: _state.metricName,
                empty: l.wizardChooseMetric,
                onTap: () async {
                  final picked = await pickOne(
                    context,
                    title: l.wizardMetric,
                    options: schema.metricNames(),
                  );
                  if (picked != null) {
                    setState(() => _state.metricName = picked);
                  }
                },
              ),
            ],
            SwitchListTile(
              key: const Key('wizard-timeseries'),
              contentPadding: EdgeInsets.zero,
              dense: true,
              title: Text(l.wizardTimeseries),
              value: _state.timeseries,
              onChanged: (v) => setState(() => _state.timeseries = v),
            ),

            const SizedBox(height: 4),
            Text(l.wizardFilters, style: theme.textTheme.labelLarge),
            for (var i = 0; i < _state.conditions.length; i++)
              _ConditionRow(
                key: ValueKey('wizard-condition-$i'),
                index: i,
                condition: _state.conditions[i],
                keys: keys,
                values: _signalOf[_state.eventType] == null
                    ? null
                    : _fieldsOf(_signalOf[_state.eventType]!),
                metric: _state.eventType == 'Metric' ? _state.metricName : '',
                onChanged: () => setState(() {}),
                onRemove: () => setState(() => _state.conditions.removeAt(i)),
              ),
            Align(
              alignment: Alignment.centerLeft,
              child: TextButton.icon(
                key: const Key('wizard-add-filter'),
                onPressed: _state.conditions.length >= maxConditions
                    ? null
                    : () => setState(
                        () => _state.conditions.add(
                          BuilderCondition(key: keys.isEmpty ? '' : keys.first),
                        ),
                      ),
                icon: const Icon(Icons.add, size: 18),
                label: Text(l.wizardAddFilter),
              ),
            ),

            const SizedBox(height: 4),
            Text(l.wizardGroupBy, style: theme.textTheme.labelLarge),
            Text(
              l.wizardGroupByHint,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
            if (_state.groupBy.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(top: 6),
                child: Wrap(
                  spacing: 6,
                  runSpacing: 4,
                  children: [
                    for (final k in _state.groupBy)
                      InputChip(
                        key: Key('wizard-group-$k'),
                        label: Text(k),
                        onDeleted: () =>
                            setState(() => _state.groupBy.remove(k)),
                      ),
                  ],
                ),
              ),
            Wrap(
              spacing: 8,
              runSpacing: 4,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                TextButton.icon(
                  key: const Key('wizard-add-group'),
                  onPressed: _state.groupBy.length >= maxGroupBy
                      ? null
                      : () async {
                          final picked = await pickOne(
                            context,
                            title: l.wizardGroupBy,
                            options: [
                              for (final k in keys)
                                if (!_state.groupBy.contains(k)) k,
                            ],
                          );
                          if (picked != null) {
                            setState(() => _state.groupBy.add(picked));
                          }
                        },
                  icon: const Icon(Icons.add, size: 18),
                  label: Text(l.wizardAddGroupBy),
                ),
                if (_state.groupBy.isNotEmpty)
                  SizedBox(
                    width: 110,
                    child: TextFormField(
                      key: const Key('wizard-limit'),
                      initialValue: '${_state.limit}',
                      keyboardType: TextInputType.number,
                      decoration: InputDecoration(
                        labelText: l.wizardLimit,
                        border: const OutlineInputBorder(),
                        isDense: true,
                      ),
                      onChanged: (v) => setState(
                        () => _state.limit = int.tryParse(v.trim()) ?? 1,
                      ),
                    ),
                  ),
              ],
            ),

            const SizedBox(height: 12),
            Container(
              width: double.infinity,
              padding: const EdgeInsets.all(10),
              decoration: BoxDecoration(
                color: theme.colorScheme.surfaceContainerHighest,
                borderRadius: BorderRadius.circular(8),
              ),
              child: Text(
                // What will run, or why it cannot yet. A preview that said
                // nothing would leave the disabled button unexplained.
                query.isEmpty
                    ? (issue == 'metric'
                          ? l.wizardMissingMetric
                          : l.wizardMissingAttribute)
                    : query,
                key: const Key('wizard-preview'),
                style: theme.textTheme.bodySmall?.copyWith(
                  fontFeatures: const [FontFeature.tabularFigures()],
                  color: query.isEmpty
                      ? theme.colorScheme.onSurfaceVariant
                      : null,
                ),
              ),
            ),
            const SizedBox(height: 8),
            // Wrap, not Row: "Sorguyu çalıştır" next to "Baştan başla" is
            // wider than a narrow phone in Turkish.
            Wrap(
              spacing: 8,
              runSpacing: 4,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                FilledButton(
                  key: const Key('wizard-run'),
                  onPressed: query.isEmpty ? null : () => widget.onRun(query),
                  child: Text(l.wizardRun),
                ),
                TextButton(
                  key: const Key('wizard-reset'),
                  onPressed: () => setState(() => _state = BuilderState()),
                  child: Text(l.wizardReset),
                ),
              ],
            ),
          ],
        );
      },
    );
  }
}

String _eventTypeLabel(L l, String name) => switch (name) {
  'Log' => l.wizardTypeLog,
  'Span' => l.wizardTypeSpan,
  'Transaction' => l.wizardTypeTransaction,
  'Metric' => l.wizardTypeMetric,
  'Host' => l.wizardTypeHost,
  _ => l.wizardTypeContainer,
};

String _measureLabel(L l, String m) => switch (m) {
  'count' => l.wizardMeasureCount,
  'average' => l.wizardMeasureAverage,
  'sum' => l.wizardMeasureSum,
  'min' => l.wizardMeasureMin,
  'max' => l.wizardMeasureMax,
  'uniqueCount' => l.wizardMeasureUnique,
  'median' => l.wizardMeasureMedian,
  'percentile' => l.wizardMeasurePercentile,
  _ => l.wizardMeasureLatest,
};

String opLabel(L l, String op) => switch (op) {
  '=' => l.wizardOpEq,
  '!=' => l.wizardOpNeq,
  'contains' => l.wizardOpContains,
  'like' => l.wizardOpLike,
  '>' => l.wizardOpGt,
  '>=' => l.wizardOpGte,
  '<' => l.wizardOpLt,
  '<=' => l.wizardOpLte,
  'is_null' => l.wizardOpNull,
  _ => l.wizardOpNotNull,
};

class _Dropdown extends StatelessWidget {
  const _Dropdown({
    required this.fieldKey,
    required this.label,
    required this.value,
    required this.items,
    required this.onChanged,
  });

  final Key fieldKey;
  final String label;
  final String value;
  final Map<String, String> items;
  final ValueChanged<String> onChanged;

  @override
  Widget build(BuildContext context) => DropdownButtonFormField<String>(
    key: fieldKey,
    initialValue: value,
    isExpanded: true,
    decoration: InputDecoration(
      labelText: label,
      border: const OutlineInputBorder(),
      isDense: true,
    ),
    items: [
      for (final e in items.entries)
        DropdownMenuItem(value: e.key, child: Text(e.value)),
    ],
    onChanged: (v) {
      if (v != null) onChanged(v);
    },
  );
}

/// A label and what was picked, which opens the picker.
class _PickRow extends StatelessWidget {
  const _PickRow({
    required this.rowKey,
    required this.label,
    required this.value,
    required this.empty,
    required this.onTap,
  });

  final Key rowKey;
  final String label;
  final String value;
  final String empty;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return InkWell(
      key: rowKey,
      onTap: onTap,
      child: InputDecorator(
        decoration: InputDecoration(
          labelText: label,
          border: const OutlineInputBorder(),
          isDense: true,
          suffixIcon: const Icon(Icons.arrow_drop_down),
        ),
        child: Text(
          value.isEmpty ? empty : value,
          overflow: TextOverflow.ellipsis,
          style: value.isEmpty
              ? theme.textTheme.bodyMedium?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                )
              : theme.textTheme.bodyMedium,
        ),
      ),
    );
  }
}

class _ConditionRow extends StatefulWidget {
  const _ConditionRow({
    super.key,
    required this.index,
    required this.condition,
    required this.keys,
    required this.values,
    required this.metric,
    required this.onChanged,
    required this.onRemove,
  });

  final int index;
  final BuilderCondition condition;
  final List<String> keys;

  /// Where the value suggestions come from, or null for an event type that
  /// has none.
  final FieldsController? values;
  final String metric;
  final VoidCallback onChanged;
  final VoidCallback onRemove;

  @override
  State<_ConditionRow> createState() => _ConditionRowState();
}

class _ConditionRowState extends State<_ConditionRow> {
  late final TextEditingController _value = TextEditingController(
    text: widget.condition.value,
  );

  @override
  void dispose() {
    _value.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.condition;
    final needsValue = c.op != 'is_null' && c.op != 'is_not_null';

    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Column(
        children: [
          Row(
            children: [
              Expanded(
                child: _PickRow(
                  rowKey: Key('wizard-condition-key-${widget.index}'),
                  label: l.wizardFilterKey,
                  value: c.key,
                  empty: l.wizardFilterKey,
                  onTap: () async {
                    final picked = await pickOne(
                      context,
                      title: l.wizardFilterKey,
                      options: widget.keys,
                    );
                    if (picked != null) {
                      c.key = picked;
                      widget.onChanged();
                    }
                  },
                ),
              ),
              IconButton(
                key: Key('wizard-remove-condition-${widget.index}'),
                tooltip: l.wizardRemoveFilter,
                onPressed: widget.onRemove,
                icon: const Icon(Icons.close),
              ),
            ],
          ),
          const SizedBox(height: 6),
          Row(
            children: [
              Expanded(
                child: _Dropdown(
                  fieldKey: Key('wizard-condition-op-${widget.index}'),
                  label: l.wizardFilterOp,
                  value: c.op,
                  items: {for (final op in builderOps) op: opLabel(l, op)},
                  onChanged: (v) {
                    c.op = v;
                    widget.onChanged();
                  },
                ),
              ),
              if (needsValue) ...[
                const SizedBox(width: 8),
                Expanded(
                  child: TextField(
                    key: Key('wizard-condition-value-${widget.index}'),
                    controller: _value,
                    autocorrect: false,
                    decoration: InputDecoration(
                      labelText: l.wizardFilterValue,
                      border: const OutlineInputBorder(),
                      isDense: true,
                      // What this key actually holds, from the same
                      // dictionary the explorers use. Typed values still
                      // work: a value nobody sent yet is a fair question.
                      suffixIcon: widget.values == null
                          ? null
                          : IconButton(
                              key: Key('wizard-values-${widget.index}'),
                              tooltip: l.wizardFilterValue,
                              icon: const Icon(Icons.list, size: 20),
                              onPressed: () async {
                                final picked = await pickValue(
                                  context,
                                  fields: widget.values!,
                                  key: c.key,
                                  metric: widget.metric,
                                );
                                if (picked == null) return;
                                _value.text = picked;
                                c.value = picked;
                                widget.onChanged();
                              },
                            ),
                    ),
                    onChanged: (v) {
                      c.value = v;
                      widget.onChanged();
                    },
                  ),
                ),
              ],
            ],
          ),
        ],
      ),
    );
  }
}

/// One of a list that can be long: a search box over it, because an
/// attribute list is hundreds of rows and a dropdown would be a scroll.
Future<String?> pickOne(
  BuildContext context, {
  required String title,
  required List<String> options,
}) => showModalBottomSheet<String>(
  context: context,
  isScrollControlled: true,
  useSafeArea: true,
  builder: (context) => _PickSheet(title: title, options: options),
);

class _PickSheet extends StatefulWidget {
  const _PickSheet({required this.title, required this.options});

  final String title;
  final List<String> options;

  @override
  State<_PickSheet> createState() => _PickSheetState();
}

class _PickSheetState extends State<_PickSheet> {
  String _q = '';

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final shown = [
      for (final o in widget.options)
        if (_q.isEmpty || o.toLowerCase().contains(_q.toLowerCase())) o,
    ];

    return Padding(
      padding: EdgeInsets.only(
        left: 16,
        right: 16,
        top: 16,
        bottom: MediaQuery.viewInsetsOf(context).bottom + 16,
      ),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(widget.title, style: theme.textTheme.titleMedium),
          const SizedBox(height: 8),
          TextField(
            key: const Key('pick-search'),
            autofocus: false,
            decoration: InputDecoration(
              hintText: l.sectionSearch,
              prefixIcon: const Icon(Icons.search),
              border: const OutlineInputBorder(),
              isDense: true,
            ),
            onChanged: (v) => setState(() => _q = v),
          ),
          const SizedBox(height: 8),
          if (shown.isEmpty)
            Padding(
              padding: const EdgeInsets.symmetric(vertical: 16),
              child: Text(
                l.wizardNoOptions,
                key: const Key('pick-empty'),
                style: theme.textTheme.bodyMedium?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
            )
          else
            Flexible(
              child: ListView.separated(
                shrinkWrap: true,
                itemCount: shown.length,
                separatorBuilder: (context, _) => const Divider(height: 1),
                itemBuilder: (context, i) => ListTile(
                  key: Key('pick-${shown[i]}'),
                  dense: true,
                  title: Text(shown[i]),
                  onTap: () => Navigator.of(context).pop(shown[i]),
                ),
              ),
            ),
        ],
      ),
    );
  }
}

/// The values one key actually holds, with their counts.
Future<String?> pickValue(
  BuildContext context, {
  required FieldsController fields,
  required String key,
  String metric = '',
}) async {
  if (key.isEmpty) return null;
  final load = fields.loadValues(key, metric: metric);
  if (!context.mounted) return null;
  return showModalBottomSheet<String>(
    context: context,
    isScrollControlled: true,
    useSafeArea: true,
    builder: (context) =>
        _ValueSheet(fields: fields, loading: load, fieldKey: key),
  );
}

class _ValueSheet extends StatelessWidget {
  const _ValueSheet({
    required this.fields,
    required this.loading,
    required this.fieldKey,
  });

  final FieldsController fields;
  final Future<void> loading;
  final String fieldKey;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.all(16),
      child: ListenableBuilder(
        listenable: fields,
        builder: (context, _) {
          final l = L.of(context);
          return Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(fieldKey, style: theme.textTheme.titleMedium),
              if (fields.loading)
                const Padding(
                  padding: EdgeInsets.symmetric(vertical: 24),
                  child: Center(child: CircularProgressIndicator()),
                )
              else if (fields.values.isEmpty)
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 16),
                  child: Text(
                    l.wizardNoOptions,
                    style: theme.textTheme.bodyMedium?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                )
              else
                Flexible(
                  child: ListView.separated(
                    shrinkWrap: true,
                    itemCount: fields.values.length,
                    separatorBuilder: (context, _) => const Divider(height: 1),
                    itemBuilder: (context, i) {
                      final v = fields.values[i];
                      return ListTile(
                        key: Key('value-${v.value}'),
                        dense: true,
                        title: Text(v.value),
                        trailing: Text(
                          '${v.count}',
                          style: theme.textTheme.bodySmall?.copyWith(
                            color: theme.colorScheme.onSurfaceVariant,
                          ),
                        ),
                        onTap: () => Navigator.of(context).pop(v.value),
                      );
                    },
                  ),
                ),
            ],
          );
        },
      ),
    );
  }
}
