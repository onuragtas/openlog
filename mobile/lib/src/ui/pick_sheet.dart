// One of a list that can be long, picked from a sheet with a search box.
//
// A dropdown is fine for six measures and useless for four hundred attribute
// names or every namespace of a cluster. The sheet is the same shape
// wherever it is used, so the OQL builder and the Kubernetes filters do not
// each invent their own.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';

/// One of a list that can be long: a search box over it, because an
/// attribute list is hundreds of rows and a dropdown would be a scroll.
Future<String?> pickOne(
  BuildContext context, {
  required String title,
  required List<String> options,
  String? firstLabel,
}) => showModalBottomSheet<String>(
  context: context,
  isScrollControlled: true,
  useSafeArea: true,
  builder: (context) =>
      _PickSheet(title: title, options: options, firstLabel: firstLabel),
);

class _PickSheet extends StatefulWidget {
  const _PickSheet({
    required this.title,
    required this.options,
    this.firstLabel,
  });

  final String title;
  final List<String> options;

  /// A row above the list that answers with an empty string -- "every one
  /// of them", which is how a filter is cleared.
  final String? firstLabel;

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
          else ...[
            if (widget.firstLabel != null && _q.isEmpty)
              ListTile(
                key: const Key('pick-all'),
                dense: true,
                title: Text(widget.firstLabel!),
                onTap: () => Navigator.of(context).pop(''),
              ),
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
        ],
      ),
    );
  }
}
