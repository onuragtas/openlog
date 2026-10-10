// One log record, in full -- and the one before it, and the one after.
//
// The web opens a side panel; a phone has no side, so this is a screen of
// its own. Two differences from a plain detail page, both because reading
// logs is reading a sequence: the records of the list are pages here, so
// the next one is a swipe rather than back-tap-open, and the record as
// JSON is a fold above the field list rather than a second tab -- a tab
// spends a line of a phone screen asking a question whose answer is
// almost always "the fields".
//
// What a record is made of is in `log_fields.dart`, which is the web's
// own list: row fields, attributes, resource attributes and the keys of a
// JSON body.
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

/// Opens [records] at [index], and tells the caller what to do with a
/// field of whichever record is on screen.
Future<void> openLogDetail(
  BuildContext context, {
  required SessionController session,
  required Sections sections,
  required List<LogQueryRow> records,
  required int index,
  required void Function(Filter filter) onFilter,
  List<String> columns = const [],
  void Function(String key)? onToggleColumn,
}) => Navigator.of(context).push(
  MaterialPageRoute<void>(
    builder: (_) => LogDetailScreen(
      session: session,
      sections: sections,
      records: records,
      index: index,
      onFilter: onFilter,
      columns: columns,
      onToggleColumn: onToggleColumn,
    ),
  ),
);

class LogDetailScreen extends StatefulWidget {
  const LogDetailScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.records,
    required this.index,
    required this.onFilter,
    this.columns = const [],
    this.onToggleColumn,
  });

  final SessionController session;
  final Sections sections;

  /// The page of records the list is showing, so moving between them is a
  /// swipe. They are already in hand; this costs nothing but the pages.
  final List<LogQueryRow> records;

  /// Which one was tapped.
  final int index;

  final void Function(Filter filter) onFilter;

  /// The table's columns, so a field can say whether it is one. Empty
  /// where the list has no table of its own -- a scoped list, say -- and
  /// then the action is not offered at all.
  final List<String> columns;
  final void Function(String key)? onToggleColumn;

  @override
  State<LogDetailScreen> createState() => _LogDetailScreenState();
}

class _LogDetailScreenState extends State<LogDetailScreen> {
  late final PageController _pages = PageController(initialPage: widget.index);
  late int _index = widget.index;

  /// The field search, shared by every page: somebody looking for
  /// `http.status_code` is looking for it in this record *and the next
  /// one*, and retyping it per record is the opposite of a sequence.
  String _query = '';

  @override
  void dispose() {
    _pages.dispose();
    super.dispose();
  }

  void _step(int delta) {
    final next = _index + delta;
    if (next < 0 || next >= widget.records.length) return;
    _pages.animateToPage(
      next,
      duration: const Duration(milliseconds: 200),
      curve: Curves.easeOut,
    );
  }

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

  /// The types the dictionary knows, so a number filters as a number.
  /// Empty until the filter builder has been opened once, which only
  /// costs the `severity_number` special case in [valueFilter].
  Map<String, FieldType> get _types => {
    for (final k in widget.sections.fields('logs').keys) k.key: k.type,
  };

  void _onAction(RecordField f, _FieldAction action) {
    final l = L.of(context);
    switch (action) {
      case _FieldAction.filterIn:
        _filter(f, exclude: false);
      case _FieldAction.filterOut:
        _filter(f, exclude: true);
      case _FieldAction.column:
        final had = widget.columns.contains(f.key);
        widget.onToggleColumn?.call(f.key);
        // The screen stays open: a column is about the list behind it,
        // and somebody adding three of them should not have to reopen
        // the record twice.
        setState(() {});
        ScaffoldMessenger.of(context).showSnackBar(
          SnackBar(
            key: const Key('log-column-toggled'),
            content: Text(
              had
                  ? l.logDetailColumnRemoved(f.key)
                  : l.logDetailColumnAdded(f.key),
            ),
          ),
        );
      case _FieldAction.copy:
        copyToClipboard(context, f.value);
    }
  }

  void _openTrace(LogQueryRow record, {String spanId = ''}) =>
      Navigator.of(context).push(
        MaterialPageRoute<void>(
          builder: (_) => TraceScreen(
            session: widget.session,
            sections: widget.sections,
            traceId: record.traceId,
            spanId: spanId,
          ),
        ),
      );

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final total = widget.records.length;

    return Scaffold(
      appBar: AppBar(
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(l.logDetailTitle),
            // Which of how many: a swipe that lands on a record looking
            // like the last one has to say that it moved.
            Text(
              l.logDetailPosition(_index + 1, total),
              style: theme.textTheme.labelSmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          ],
        ),
        actions: [
          IconButton(
            key: const Key('log-prev'),
            tooltip: l.logDetailPrev,
            onPressed: _index > 0 ? () => _step(-1) : null,
            icon: const Icon(Icons.keyboard_arrow_up),
          ),
          IconButton(
            key: const Key('log-next'),
            tooltip: l.logDetailNext,
            onPressed: _index < total - 1 ? () => _step(1) : null,
            icon: const Icon(Icons.keyboard_arrow_down),
          ),
        ],
      ),
      // Swiping sideways is the gesture a phone already has for "the next
      // one of these", and the field list scrolls the other way, so the
      // two never fight.
      body: PageView.builder(
        key: const Key('log-pages'),
        controller: _pages,
        itemCount: total,
        onPageChanged: (i) => setState(() => _index = i),
        itemBuilder: (context, i) => _RecordView(
          record: widget.records[i],
          query: _query,
          onQuery: (q) => _query = q,
          columns: widget.columns,
          canColumn: widget.onToggleColumn != null,
          onAction: _onAction,
          onOpenTrace: (spanId) =>
              _openTrace(widget.records[i], spanId: spanId),
        ),
      ),
    );
  }
}

/// Copies [value] and says so, from wherever it was asked for.
void copyToClipboard(BuildContext context, String value) {
  final messenger = ScaffoldMessenger.of(context);
  final said = L.of(context).logDetailCopied;
  Clipboard.setData(ClipboardData(text: value));
  messenger.showSnackBar(
    SnackBar(key: const Key('log-copied'), content: Text(said)),
  );
}

enum _FieldAction { filterIn, filterOut, column, copy }

/// One page: the record's head, its JSON behind a fold, and its fields.
class _RecordView extends StatefulWidget {
  const _RecordView({
    required this.record,
    required this.query,
    required this.onQuery,
    required this.columns,
    required this.canColumn,
    required this.onAction,
    required this.onOpenTrace,
  });

  final LogQueryRow record;

  /// The field search as it stands, so a page built after a swipe opens
  /// already filtered.
  final String query;
  final ValueChanged<String> onQuery;

  final List<String> columns;
  final bool canColumn;
  final void Function(RecordField field, _FieldAction action) onAction;
  final void Function(String spanId) onOpenTrace;

  @override
  State<_RecordView> createState() => _RecordViewState();
}

class _RecordViewState extends State<_RecordView> {
  late final TextEditingController _search = TextEditingController(
    text: widget.query,
  );
  late String _query = widget.query.toLowerCase();

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final fields = recordFields(widget.record);
    final shown = _query.isEmpty
        ? fields
        : [
            for (final f in fields)
              if (f.key.toLowerCase().contains(_query) ||
                  f.value.toLowerCase().contains(_query))
                f,
          ];

    return ListView(
      padding: const EdgeInsets.fromLTRB(16, 16, 16, 24),
      children: [
        _head(context, l),
        _json(context, l),
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
              onChanged: (v) {
                widget.onQuery(v.trim());
                setState(() => _query = v.trim().toLowerCase());
              },
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
            for (final f in shown)
              _FieldRow(
                field: f,
                column: widget.columns.contains(f.key),
                // `timestamp` is pinned, and a field the table cannot
                // show is not offered as a column.
                canColumn: widget.canColumn && f.key != 'timestamp',
                onAction: widget.onAction,
              ),
          ],
        ),
      ],
    );
  }

  /// When, how bad, from where, and what it said.
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
              onPressed: () => copyToClipboard(context, r.body),
              icon: const Icon(Icons.copy, size: 18),
              label: Text(l.logDetailCopyBody),
            ),
            if (r.traceId.isNotEmpty)
              OutlinedButton.icon(
                key: const Key('log-open-trace'),
                onPressed: () => widget.onOpenTrace(''),
                icon: const Icon(Icons.account_tree_outlined, size: 18),
                label: Text(l.logOpenTrace),
              ),
            // The span, not only the request it was part of: the web's
            // panel links to both, and a trace of eighty spans is a list
            // to search by eye without it.
            if (r.traceId.isNotEmpty && r.spanId.isNotEmpty)
              OutlinedButton.icon(
                key: const Key('log-open-span'),
                onPressed: () => widget.onOpenTrace(r.spanId),
                icon: const Icon(Icons.linear_scale, size: 18),
                label: Text(l.logOpenSpan),
              ),
          ],
        ),
      ],
    );
  }

  /// The whole record as JSON, folded away.
  ///
  /// A fold rather than the second of two tabs: the fields are what this
  /// screen is for, and the JSON is what somebody pastes into a ticket
  /// once in a while.
  Widget _json(BuildContext context, L l) {
    final text = recordJson(widget.record);
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.only(top: 12),
      child: Theme(
        // An ExpansionTile draws divider lines around itself, which here
        // would box in the head above it; this is a fold, not a section.
        data: theme.copyWith(dividerColor: Colors.transparent),
        child: ExpansionTile(
          key: const Key('log-json'),
          tilePadding: EdgeInsets.zero,
          childrenPadding: EdgeInsets.zero,
          expandedCrossAxisAlignment: CrossAxisAlignment.start,
          title: Text(l.logDetailJson, style: theme.textTheme.titleSmall),
          children: [
            Align(
              alignment: Alignment.centerLeft,
              child: OutlinedButton.icon(
                key: const Key('log-copy-json'),
                onPressed: () => copyToClipboard(context, text),
                icon: const Icon(Icons.copy, size: 18),
                label: Text(l.logDetailCopyJson),
              ),
            ),
            const SizedBox(height: 8),
            SelectableText(
              text,
              key: const Key('log-json-text'),
              style: mono(theme.textTheme.bodySmall),
            ),
          ],
        ),
      ),
    );
  }
}

/// One key and its value, with what can be done to it behind a menu.
///
/// A menu rather than three buttons per row: a record has dozens of fields
/// and sixty tap targets on one screen is how the wrong one gets tapped.
class _FieldRow extends StatelessWidget {
  const _FieldRow({
    required this.field,
    required this.column,
    required this.canColumn,
    required this.onAction,
  });

  final RecordField field;

  /// Already a column of the list behind this screen.
  final bool column;
  final bool canColumn;
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
                Row(
                  children: [
                    if (column)
                      Padding(
                        padding: const EdgeInsets.only(right: 4),
                        child: Icon(
                          Icons.view_column_outlined,
                          size: 12,
                          color: theme.colorScheme.primary,
                        ),
                      ),
                    Flexible(
                      child: Text(
                        field.key,
                        style: theme.textTheme.labelSmall?.copyWith(
                          color: theme.colorScheme.onSurfaceVariant,
                        ),
                      ),
                    ),
                  ],
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
              if (canColumn)
                PopupMenuItem(
                  value: _FieldAction.column,
                  child: Text(
                    column ? l.logDetailRemoveColumn : l.logDetailAddColumn,
                  ),
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
