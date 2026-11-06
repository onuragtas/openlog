// The sort control and the foot of a list the server sorts and cuts.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../list_controller.dart';

/// Which column a list is sorted by, and which way. A phone has no column
/// headings to tap, so this does their work.
class ListSortBar extends StatelessWidget {
  const ListSortBar({
    super.key,
    required this.controller,
    required this.columns,
    required this.label,
  });

  final PagedListController<dynamic> controller;
  final List<String> columns;
  final String Function(String column) label;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final ascending = controller.dir == 'asc';
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        // Flexible and expanded: a dropdown is as wide as its widest item, and a
        // long column name in large text would otherwise run off a phone.
        Flexible(
          child: DropdownButton<String>(
            key: const Key('list-sort'),
            isExpanded: true,
            value: controller.sort,
            underline: const SizedBox.shrink(),
            items: [
              for (final c in columns)
                DropdownMenuItem(
                  value: c,
                  child: Text(label(c), overflow: TextOverflow.ellipsis),
                ),
            ],
            onChanged: (v) {
              if (v != null && v != controller.sort) controller.setSort(v);
            },
          ),
        ),
        IconButton(
          key: const Key('list-sort-direction'),
          tooltip: ascending ? l.listSortAscending : l.listSortDescending,
          icon: Icon(ascending ? Icons.arrow_upward : Icons.arrow_downward),
          onPressed: () => controller.setSort(controller.sort),
        ),
      ],
    );
  }
}

/// The foot of a list: more to fetch, or a failed attempt at it.
///
/// Reaching it is asking for the next page, so it fetches by itself: a list
/// builds its last row only when the person has scrolled to it. A failed
/// attempt waits for the button instead, or a phone without signal would
/// ask again on every frame.
class LoadMoreTile extends StatelessWidget {
  const LoadMoreTile({super.key, required this.controller});

  final PagedListController<dynamic> controller;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    if (!controller.hasMore) return const SizedBox.shrink();
    if (!controller.loadingMore && !controller.loadMoreFailed) {
      WidgetsBinding.instance.addPostFrameCallback(
        (_) => controller.loadMore(),
      );
    }
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: Center(
        child: controller.loadingMore
            ? const CircularProgressIndicator()
            : TextButton(
                key: const Key('list-load-more'),
                onPressed: controller.loadMore,
                child: Text(
                  controller.loadMoreFailed
                      ? l.listLoadMoreRetry
                      : l.listLoadMore,
                ),
              ),
      ),
    );
  }
}
