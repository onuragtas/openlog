// Which columns the log list shows.
//
// The web's `Sütunlar` button: the dictionary's keys with a tick beside the
// ones in the table, plus the keys already chosen that the dictionary has
// not sent (a view made in a browser can name a key that is rare in this
// window). `timestamp` is pinned -- a log line without its time is not a
// log line -- and "reset" puts the web's four defaults back.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../fields.dart';
import '../log_fields.dart';
import '../session.dart';
import 'failure_text.dart';

/// Opens the picker. Returns the chosen columns, or null when nothing was
/// changed.
Future<List<String>?> pickColumns(
  BuildContext context, {
  required SessionController session,
  required FieldsController fields,
  required List<String> columns,
}) {
  fields.loadKeys();
  return showModalBottomSheet<List<String>>(
    context: context,
    isScrollControlled: true,
    builder: (context) => Padding(
      padding: EdgeInsets.only(
        bottom: MediaQuery.of(context).viewInsets.bottom,
      ),
      child: FractionallySizedBox(
        heightFactor: 0.8,
        child: _ColumnsSheet(
          session: session,
          fields: fields,
          columns: columns,
        ),
      ),
    ),
  );
}

class _ColumnsSheet extends StatefulWidget {
  const _ColumnsSheet({
    required this.session,
    required this.fields,
    required this.columns,
  });

  final SessionController session;
  final FieldsController fields;
  final List<String> columns;

  @override
  State<_ColumnsSheet> createState() => _ColumnsSheetState();
}

class _ColumnsSheetState extends State<_ColumnsSheet> {
  final _search = TextEditingController();
  late List<String> _columns = [...widget.columns];
  String _query = '';

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  bool _matches(String text) =>
      _query.isEmpty || text.toLowerCase().contains(_query.toLowerCase());

  /// The keys to offer: the ones already chosen first, in the table's own
  /// order, then whatever the dictionary knows. Chosen first because the
  /// list is long and unticking one should not mean scrolling for it.
  List<({String key, String? note})> _offered(FieldsController f) {
    final notes = {for (final k in f.keys) k.key: k.source.wire};
    final out = <({String key, String? note})>[
      for (final c in _columns)
        if (_matches(c) || _columns.contains(c)) (key: c, note: notes[c]),
    ];
    final chosen = {..._columns};
    for (final k in f.keys) {
      if (chosen.contains(k.key) || !_matches(k.key)) continue;
      out.add((key: k.key, note: k.source.wire));
    }
    return out;
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final f = widget.fields;

    return ListenableBuilder(
      listenable: f,
      builder: (context, _) => SafeArea(
        child: Column(
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 14, 16, 6),
              child: Row(
                children: [
                  Expanded(
                    child: Text(
                      l.logColumnsPick,
                      style: theme.textTheme.titleSmall,
                    ),
                  ),
                  if (!isDefaultColumns(_columns))
                    TextButton(
                      key: const Key('columns-reset'),
                      onPressed: () =>
                          setState(() => _columns = [...defaultLogColumns]),
                      child: Text(l.logColumnsReset),
                    ),
                  FilledButton(
                    key: const Key('columns-apply'),
                    onPressed: () => Navigator.of(context).pop(_columns),
                    child: Text(l.logColumnsApply),
                  ),
                ],
              ),
            ),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 16),
              child: TextField(
                key: const Key('columns-search'),
                controller: _search,
                textInputAction: TextInputAction.search,
                autocorrect: false,
                onChanged: (q) => setState(() => _query = q),
                onSubmitted: (q) => f.loadKeys(q: q.trim()),
                decoration: InputDecoration(
                  hintText: l.filterSearchKey,
                  prefixIcon: const Icon(Icons.search),
                  border: const OutlineInputBorder(),
                  isDense: true,
                ),
              ),
            ),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 16),
              child: FailureBanner(
                failure: f.failure,
                baseUrl: widget.session.baseUrl ?? '',
              ),
            ),
            Expanded(
              child: f.loading
                  ? const Center(child: CircularProgressIndicator())
                  : ListView(
                      children: [
                        for (final o in _offered(f))
                          CheckboxListTile(
                            key: Key('column-${o.key}'),
                            dense: true,
                            value: _columns.contains(o.key),
                            // The time is always the first column, so its
                            // tick is there to be read, not tapped.
                            onChanged: o.key == 'timestamp'
                                ? null
                                : (_) => setState(
                                    () => _columns = toggleColumn(
                                      _columns,
                                      o.key,
                                    ),
                                  ),
                            title: Text(o.key),
                            subtitle: o.note == null ? null : Text(o.note!),
                          ),
                      ],
                    ),
            ),
          ],
        ),
      ),
    );
  }
}
