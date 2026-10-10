// Building a filter by picking, not by typing.
//
// Two steps, because that is what the two endpoints are: the keys the data
// has, then the values that key holds. Counts are shown next to both, which
// is the part that makes picking better than typing -- a key with three
// records behind it is rarely the one somebody wants.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../fields.dart';
import '../session.dart';
import 'failure_text.dart';

/// Opens the builder and returns the filter somebody picked, or null.
Future<Filter?> pickFilter(
  BuildContext context, {
  required SessionController session,
  required FieldsController fields,
}) {
  fields.loadKeys();
  return showModalBottomSheet<Filter>(
    context: context,
    isScrollControlled: true,
    builder: (context) => Padding(
      padding: EdgeInsets.only(
        bottom: MediaQuery.of(context).viewInsets.bottom,
      ),
      child: FractionallySizedBox(
        heightFactor: 0.8,
        child: _FilterSheet(session: session, fields: fields),
      ),
    ),
  );
}

class _FilterSheet extends StatefulWidget {
  const _FilterSheet({required this.session, required this.fields});

  final SessionController session;
  final FieldsController fields;

  @override
  State<_FilterSheet> createState() => _FilterSheetState();
}

class _FilterSheetState extends State<_FilterSheet> {
  final _search = TextEditingController();

  /// The values ticked so far. Several make an `in` filter, which is one
  /// condition rather than three the server has to OR together.
  final _picked = <String>{};

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
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
                  if (f.key.isNotEmpty)
                    IconButton(
                      key: const Key('filter-back'),
                      tooltip: l.filterBack,
                      onPressed: () {
                        _picked.clear();
                        _search.clear();
                        f.loadKeys();
                      },
                      icon: const Icon(Icons.arrow_back),
                    ),
                  Expanded(
                    child: Text(
                      f.key.isEmpty ? l.filterPickKey : f.key,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.titleSmall,
                    ),
                  ),
                  if (f.key.isNotEmpty)
                    TextButton(
                      key: const Key('filter-exists'),
                      // Not every filter is about a value: "has this key
                      // at all" is a question the contract has an operator
                      // for, and the only way to ask it here.
                      onPressed: () => Navigator.of(
                        context,
                      ).pop(Filter(key: f.key, op: 'exists')),
                      child: Text(l.filterExists),
                    ),
                ],
              ),
            ),
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 16),
              child: TextField(
                key: const Key('filter-search'),
                controller: _search,
                textInputAction: TextInputAction.search,
                onSubmitted: (q) => f.key.isEmpty
                    ? f.loadKeys(q: q.trim())
                    : f.loadValues(f.key, q: q.trim()),
                decoration: InputDecoration(
                  hintText: f.key.isEmpty
                      ? l.filterSearchKey
                      : l.filterSearchValue,
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
            if ((f.key.isEmpty && f.keysSampled) ||
                (f.key.isNotEmpty && f.valuesSampled))
              Padding(
                padding: const EdgeInsets.fromLTRB(16, 4, 16, 0),
                child: Text(
                  // The server counted a sample, not everything: the list
                  // is the most frequent, not all there is.
                  l.filterSampled,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                  ),
                ),
              ),
            Expanded(
              child: f.loading
                  ? const Center(child: CircularProgressIndicator())
                  : f.key.isEmpty
                  ? ListView(
                      children: [
                        for (final k in f.keys)
                          ListTile(
                            key: Key('filter-key-${k.key}'),
                            dense: true,
                            title: Text(k.key),
                            subtitle: Text(
                              [
                                k.source.wire,
                                if (k.count != null) l.filterRecords(k.count!),
                                if (k.cardinality != null)
                                  l.filterDistinct(k.cardinality!),
                              ].join(' · '),
                            ),
                            onTap: () => f.loadValues(k.key),
                          ),
                      ],
                    )
                  : ListView(
                      children: [
                        for (final v in f.values)
                          CheckboxListTile(
                            key: Key('filter-value-${v.value}'),
                            dense: true,
                            value: _picked.contains(v.value),
                            title: Text(
                              v.value.isEmpty ? l.filterEmptyValue : v.value,
                            ),
                            subtitle: Text(l.filterRecords(v.count)),
                            onChanged: (on) => setState(() {
                              if (on ?? false) {
                                _picked.add(v.value);
                              } else {
                                _picked.remove(v.value);
                              }
                            }),
                          ),
                      ],
                    ),
            ),
            if (f.key.isNotEmpty)
              Padding(
                padding: const EdgeInsets.all(12),
                child: Row(
                  mainAxisAlignment: MainAxisAlignment.end,
                  children: [
                    TextButton(
                      onPressed: () => Navigator.of(context).pop(),
                      child: Text(l.ruleCancel),
                    ),
                    const SizedBox(width: 8),
                    FilledButton(
                      key: const Key('filter-apply'),
                      onPressed: _picked.isEmpty
                          ? null
                          : () => Navigator.of(context).pop(
                              Filter(
                                key: f.key,
                                // One value is `eq`; several are one `in`
                                // rather than conditions the server has to
                                // OR back together.
                                op: _picked.length == 1 ? 'eq' : 'in',
                                values: _picked.toList(),
                              ),
                            ),
                      child: Text(l.filterApply),
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

/// The filters in force, as chips that can be taken off again.
class FilterChips extends StatelessWidget {
  const FilterChips({
    super.key,
    required this.filters,
    required this.onRemove,
    required this.onAdd,
  });

  final List<Filter> filters;
  final void Function(int index) onRemove;
  final VoidCallback onAdd;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return Padding(
      padding: const EdgeInsets.fromLTRB(12, 4, 12, 0),
      child: Wrap(
        spacing: 6,
        runSpacing: 4,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          for (var i = 0; i < filters.length; i++)
            InputChip(
              key: Key('filter-chip-$i'),
              label: Text(filters[i].label),
              onDeleted: () => onRemove(i),
            ),
          ActionChip(
            key: const Key('filter-add'),
            avatar: const Icon(Icons.filter_alt_outlined, size: 16),
            label: Text(l.filterAdd),
            onPressed: onAdd,
          ),
        ],
      ),
    );
  }
}
