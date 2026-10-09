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
  });

  final ListController<T> controller;
  final String baseUrl;
  final String emptyTitle;
  final Widget? search;
  final Widget? header;
  final Widget Function(BuildContext, int) itemBuilder;

  @override
  Widget build(BuildContext context) {
    final banner = FailureBanner(failure: controller.failure, baseUrl: baseUrl);

    return RefreshIndicator(
      onRefresh: controller.refresh,
      child: Column(
        children: [
          if (search != null)
            Padding(
              padding: const EdgeInsets.fromLTRB(12, 12, 12, 4),
              child: search,
            ),
          Expanded(
            child: controller.loadingFirst
                ? const Center(child: CircularProgressIndicator())
                : ListView.builder(
                    padding: const EdgeInsets.fromLTRB(12, 8, 12, 24),
                    // Header, rows (or the empty state), banner. Always at
                    // least two, so pull-to-refresh has something to pull on
                    // when the list is empty -- which is exactly when the
                    // person wants to check again.
                    itemCount: controller.items.isEmpty
                        ? 2
                        : controller.items.length + 2,
                    itemBuilder: (context, i) {
                      if (i == 0) return header ?? const SizedBox.shrink();
                      if (controller.items.isEmpty) {
                        return _Empty(title: emptyTitle, banner: banner);
                      }
                      if (i == controller.items.length + 1) return banner;
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
