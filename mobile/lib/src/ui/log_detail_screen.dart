// One log record, in full.
//
// The web opens a side panel; a phone has no side, so this is a screen of
// its own with the same two tabs and the same actions on every field:
// filter for it, exclude it, copy it. What a record is made of is in
// `log_fields.dart`, which is the web's own list -- row fields, attributes,
// resource attributes and the keys of a JSON body.
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../fields.dart';
import '../log_fields.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'list_scaffold.dart' show relativeTimeOf;
import 'severity.dart';
import 'theme.dart';
import 'trace_screen.dart';

/// Opens the record, and tells the caller what to do with it.
///
/// [onFilter] is what the field menu's "filter for this" ends up calling:
/// the list owns its conditions, not this screen.
Future<void> openLogDetail(
  BuildContext context, {
  required SessionController session,
  required Sections sections,
  required LogQueryRow record,
  required void Function(Filter filter) onFilter,
}) => Navigator.of(context).push(
  MaterialPageRoute<void>(
    builder: (_) => LogDetailScreen(
      session: session,
      sections: sections,
      record: record,
      onFilter: onFilter,
    ),
  ),
);

class LogDetailScreen extends StatefulWidget {
  const LogDetailScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.record,
    required this.onFilter,
  });

  final SessionController session;
  final Sections sections;
  final LogQueryRow record;
  final void Function(Filter filter) onFilter;

  @override
  State<LogDetailScreen> createState() => _LogDetailScreenState();
}

class _LogDetailScreenState extends State<LogDetailScreen>
    with SingleTickerProviderStateMixin {
  late final TabController _tabs = TabController(length: 2, vsync: this);
  final _search = TextEditingController();

  /// What was typed in the field search, lower-cased once rather than on
  /// every row of a record that can carry a hundred attributes.
  String _query = '';

  @override
  void dispose() {
    _tabs.dispose();
    _search.dispose();
    super.dispose();
  }

  /// The types the dictionary knows, so a number filters as a number.
  /// Empty until the filter builder has been opened once, which only
  /// costs the `severity_number` special case below.
  Map<String, FieldType> get _types => {
    for (final k in widget.sections.fields('logs').keys) k.key: k.type,
  };

  void _filter(RecordField f, {required bool exclude}) {
    final l = L.of(context);
    final messenger = ScaffoldMessenger.of(context);
    final navigator = Navigator.of(context);
    // The server refuses a condition whose value is over 1024 bytes, and
    // a log body or a stack trace in an attribute reaches that.
    if (!isFilterableValue(f.value)) {
      messenger.showSnackBar(
        SnackBar(
          key: const Key('log-value-too-long'),
          content: Text(l.logDetailValueTooLong),
        ),
      );
      return;
    }
    final filter = valueFilter(
      f.key,
      f.value,
      exclude: exclude,
      type: _types[f.key],
    );
    widget.onFilter(filter);
    // Back to the list, because the list is where the answer is. The web
    // keeps its panel open beside the rows; a phone would be showing the
    // record over the list it just changed.
    navigator.pop();
    messenger.showSnackBar(
      SnackBar(
        key: const Key('log-filter-added'),
        content: Text(l.logDetailFilterAdded(filter.label)),
      ),
    );
  }

  Future<void> _copy(String value, String said) async {
    final messenger = ScaffoldMessenger.of(context);
    await Clipboard.setData(ClipboardData(text: value));
    messenger.showSnackBar(
      SnackBar(key: const Key('log-copied'), content: Text(said)),
    );
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final r = widget.record;
    final fields = recordFields(r);
    final shown = _query.isEmpty
        ? fields
        : [
            for (final f in fields)
              if (f.key.toLowerCase().contains(_query) ||
                  f.value.toLowerCase().contains(_query))
                f,
          ];

    return Scaffold(
      appBar: AppBar(
        title: Text(l.logDetailTitle),
        bottom: TabBar(
          controller: _tabs,
          tabs: [
            Tab(key: const Key('log-tab-fields'), text: l.logDetailFields),
            Tab(key: const Key('log-tab-json'), text: l.logDetailJson),
          ],
        ),
      ),
      body: TabBarView(
        controller: _tabs,
        children: [_fields(context, l, shown), _json(context, l)],
      ),
    );
  }

  /// The head of both tabs: when, how bad, from where, and what it said.
  Widget _head(BuildContext context, L l) {
    final r = widget.record;
    final theme = Theme.of(context);
    final level = severityOfNumber(r.severityNumber);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Wrap(
          spacing: 10,
          runSpacing: 4,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            Text(
              r.timestamp.toLocal().toString().split('.').first,
              style: theme.textTheme.bodySmall?.copyWith(
                fontFeatures: const [FontFeature.tabularFigures()],
              ),
            ),
            Text(
              r.severityText.isEmpty
                  ? (r.severityNumber == 0
                        ? l.logsNoSeverity
                        : '${r.severityNumber}')
                  : r.severityText,
              style: theme.textTheme.labelMedium?.copyWith(
                color: severityTextColor(context, level),
                fontWeight: FontWeight.w700,
              ),
            ),
            Text(
              relativeTimeOf(l, r.timestamp),
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          ],
        ),
        if (r.serviceName.isNotEmpty || r.hostName.isNotEmpty)
          Padding(
            padding: const EdgeInsets.only(top: 4),
            child: Text(
              [
                if (r.serviceName.isNotEmpty) r.serviceName,
                if (r.hostName.isNotEmpty) r.hostName,
              ].join(' · '),
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          ),
        const SizedBox(height: 10),
        // Selectable, because half of what anybody does with a log line on
        // a phone is send it to somebody else.
        Container(
          width: double.infinity,
          padding: const EdgeInsets.all(10),
          decoration: BoxDecoration(
            color: theme.colorScheme.surfaceContainerHighest,
            borderRadius: BorderRadius.circular(8),
          ),
          child: SelectableText(
            r.body,
            key: const Key('log-body'),
            style: mono(theme.textTheme.bodySmall),
          ),
        ),
        const SizedBox(height: 8),
        Wrap(
          spacing: 8,
          runSpacing: 8,
          children: [
            OutlinedButton.icon(
              key: const Key('log-copy-body'),
              onPressed: () => _copy(r.body, l.logDetailCopied),
              icon: const Icon(Icons.copy, size: 18),
              label: Text(l.logDetailCopyBody),
            ),
            if (r.traceId.isNotEmpty)
              OutlinedButton.icon(
                key: const Key('log-open-trace'),
                onPressed: () => Navigator.of(context).push(
                  MaterialPageRoute<void>(
                    builder: (_) => TraceScreen(
                      session: widget.session,
                      sections: widget.sections,
                      traceId: r.traceId,
                    ),
                  ),
                ),
                icon: const Icon(Icons.account_tree_outlined, size: 18),
                label: Text(l.logOpenTrace),
              ),
          ],
        ),
      ],
    );
  }

  Widget _fields(BuildContext context, L l, List<RecordField> shown) =>
      ListView(
        padding: const EdgeInsets.fromLTRB(16, 16, 16, 24),
        children: [
          _head(context, l),
          DetailSection(
            title: l.logDetailFields,
            children: [
              TextField(
                key: const Key('log-field-search'),
                controller: _search,
                autocorrect: false,
                decoration: InputDecoration(
                  labelText: l.logDetailSearch,
                  border: const OutlineInputBorder(),
                  isDense: true,
                  prefixIcon: const Icon(Icons.search, size: 20),
                ),
                onChanged: (v) =>
                    setState(() => _query = v.trim().toLowerCase()),
              ),
              const SizedBox(height: 8),
              if (shown.isEmpty)
                Padding(
                  padding: const EdgeInsets.symmetric(vertical: 12),
                  child: Text(
                    l.logDetailNoFields,
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                ),
              for (final f in shown) _FieldRow(field: f, onAction: _onAction),
            ],
          ),
        ],
      );

  void _onAction(RecordField f, _FieldAction action) {
    switch (action) {
      case _FieldAction.filterIn:
        _filter(f, exclude: false);
      case _FieldAction.filterOut:
        _filter(f, exclude: true);
      case _FieldAction.copy:
        _copy(f.value, L.of(context).logDetailCopied);
    }
  }

  Widget _json(BuildContext context, L l) {
    final text = recordJson(widget.record);
    return ListView(
      padding: const EdgeInsets.fromLTRB(16, 16, 16, 24),
      children: [
        Align(
          alignment: Alignment.centerLeft,
          child: OutlinedButton.icon(
            key: const Key('log-copy-json'),
            onPressed: () => _copy(text, l.logDetailCopied),
            icon: const Icon(Icons.copy, size: 18),
            label: Text(l.logDetailCopyJson),
          ),
        ),
        const SizedBox(height: 10),
        SelectableText(
          text,
          key: const Key('log-json'),
          style: mono(Theme.of(context).textTheme.bodySmall),
        ),
      ],
    );
  }
}

enum _FieldAction { filterIn, filterOut, copy }

/// One key and its value, with what can be done to it behind a menu.
///
/// A menu rather than three buttons per row: a record has dozens of fields
/// and sixty tap targets on one screen is how the wrong one gets tapped.
class _FieldRow extends StatelessWidget {
  const _FieldRow({required this.field, required this.onAction});

  final RecordField field;
  final void Function(RecordField field, _FieldAction action) onAction;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  field.key,
                  style: theme.textTheme.labelSmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
                SelectableText(
                  field.value,
                  style: mono(theme.textTheme.bodySmall),
                ),
              ],
            ),
          ),
          PopupMenuButton<_FieldAction>(
            key: Key('log-field-${field.key}'),
            tooltip: field.key,
            icon: const Icon(Icons.more_vert, size: 20),
            onSelected: (a) => onAction(field, a),
            itemBuilder: (context) => [
              PopupMenuItem(
                value: _FieldAction.filterIn,
                child: Text(l.logDetailFilterIn),
              ),
              PopupMenuItem(
                value: _FieldAction.filterOut,
                child: Text(l.logDetailFilterOut),
              ),
              PopupMenuItem(
                value: _FieldAction.copy,
                child: Text(l.logDetailCopy),
              ),
            ],
          ),
        ],
      ),
    );
  }
}
