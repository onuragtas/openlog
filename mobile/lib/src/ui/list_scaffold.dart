// The shape every list screen shares: a search area, rows, an empty state, a
// failure banner and pull-to-refresh.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../list_controller.dart';
import 'failure_text.dart';

class ListScreen<T> extends StatelessWidget {
  const ListScreen({
    super.key,
    required this.controller,
    required this.baseUrl,
    required this.emptyTitle,
    required this.itemBuilder,
    this.search,
    this.header,
    this.itemCount,
  });

  final ListController<T> controller;
  final String baseUrl;
  final String emptyTitle;
  final Widget? search;
  final Widget? header;
  final Widget Function(BuildContext, int) itemBuilder;

  /// How many rows to draw, when a row is not one item: the containers
  /// list draws one row per compose service when it is grouped. Null means
  /// one row per item, which is every other list.
  final int? itemCount;

  @override
  Widget build(BuildContext context) {
    final banner = FailureBanner(failure: controller.failure, baseUrl: baseUrl);

    return RefreshIndicator(
      onRefresh: controller.refresh,
      // One scroll view, with the search area inside it rather than nailed
      // above it. A phone screen is about ten log lines tall and the logs
      // search area -- box, service, views, conditions, chart, table
      // options, severity -- is a third of it; keeping that on screen
      // while reading means reading the list through a slot.
      //
      // A floating header rather than a plain first row: scrolling down
      // gets it out of the way, and the smallest pull back up returns it,
      // so changing a filter never means scrolling to the top first.
      child: CustomScrollView(
        slivers: [
          if (search != null)
            SliverFloatingHeader(
              child: ColoredBox(
                // The rows scroll under it, so it cannot be transparent.
                color: Theme.of(context).colorScheme.surface,
                child: Padding(
                  padding: const EdgeInsets.fromLTRB(12, 12, 12, 4),
                  child: search,
                ),
              ),
            ),
          if (controller.loadingFirst)
            const SliverFillRemaining(
              hasScrollBody: false,
              child: Center(child: CircularProgressIndicator()),
            )
          else
            SliverPadding(
              padding: const EdgeInsets.fromLTRB(12, 8, 12, 24),
              sliver: SliverList.builder(
                // Header, rows (or the empty state), banner. Always at
                // least two, so pull-to-refresh has something to pull on
                // when the list is empty -- which is exactly when the
                // person wants to check again.
                itemCount: controller.items.isEmpty
                    ? 2
                    : (itemCount ?? controller.items.length) + 2,
                itemBuilder: (context, i) {
                  if (i == 0) {
                    // Above the rows, not under them: a refresh that
                    // failed leaves the previous rows on screen, and
                    // an explanation at the end of a long list is an
                    // explanation nobody reads.
                    return Column(
                      crossAxisAlignment: CrossAxisAlignment.stretch,
                      children: [
                        if (controller.items.isNotEmpty) banner,
                        header ?? const SizedBox.shrink(),
                      ],
                    );
                  }
                  if (controller.items.isEmpty) {
                    return _Empty(title: emptyTitle, banner: banner);
                  }
                  final rows = itemCount ?? controller.items.length;
                  if (i == rows + 1) return const SizedBox.shrink();
                  return itemBuilder(context, i - 1);
                },
              ),
            ),
        ],
      ),
    );
  }
}

class _Empty extends StatelessWidget {
  const _Empty({required this.title, required this.banner});

  final String title;
  final Widget banner;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 12),
      child: Column(
        children: [
          const SizedBox(height: 64),
          Text(
            title,
            textAlign: TextAlign.center,
            style: Theme.of(context).textTheme.titleMedium,
          ),
          banner,
        ],
      ),
    );
  }
}

/// A search box that searches when the person says so, not on every keystroke:
/// each of these is a query against the installation, and a phone keyboard
/// would fire one per letter.
class SearchField extends StatelessWidget {
  const SearchField({
    super.key,
    required this.fieldKey,
    required this.controller,
    required this.hint,
    required this.onSubmitted,
  });

  final Key fieldKey;
  final TextEditingController controller;
  final String hint;
  final ValueChanged<String> onSubmitted;

  @override
  Widget build(BuildContext context) {
    return TextField(
      key: fieldKey,
      controller: controller,
      textInputAction: TextInputAction.search,
      onSubmitted: onSubmitted,
      decoration: InputDecoration(
        hintText: hint,
        prefixIcon: const Icon(Icons.search),
        border: const OutlineInputBorder(),
        isDense: true,
      ),
    );
  }
}

/// "3 dk önce", or "2 sa sonra". Coarse on purpose: how long something has
/// been going matters more than its exact second, and a phone screen has
/// little room.
///
/// Both directions, because not every timestamp is in the past: a mute ends
/// later, a rule is evaluated next at some point. This read every future time
/// as "just now" until a mute window made it obvious.
String relativeTimeOf(L l, DateTime at) {
  final d = DateTime.now().toUtc().difference(at.toUtc());
  if (d.isNegative) {
    final ahead = -d;
    // Minutes round up, because four minutes and fifty-nine seconds is five
    // and truncating it says the deadline is nearer than it is. Hours and
    // days truncate, like the past does: rounding ninety minutes up to two
    // hours overstates by more than it clarifies.
    if (ahead.inSeconds < 60) return l.rightNow;
    if (ahead.inMinutes < 60) return l.inMinutes((ahead.inSeconds / 60).ceil());
    if (ahead.inHours < 48) return l.inHours(ahead.inHours);
    return l.inDays(ahead.inDays);
  }
  if (d.inMinutes < 1) return l.justNow;
  if (d.inMinutes < 60) return l.minutesAgo(d.inMinutes);
  if (d.inHours < 48) return l.hoursAgo(d.inHours);
  return l.daysAgo(d.inDays);
}
