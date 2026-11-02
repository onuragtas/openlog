// What crashed a mobile application (mobile-crash.md §6). A crash is an error
// span, so resolving and assigning one stays in the errors inbox. This list is
// what the inbox cannot say: which errors were fatal, on how many sessions, and
// with what stack. The server has already applied whatever symbols were
// uploaded, and says what it lacked for the rest.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../analytics.dart' show formatCount;
import '../api/schema.g.dart';
import '../rum.dart';
import '../sections.dart';
import '../session.dart';
import 'list_scaffold.dart';
import 'theme.dart';

class RumCrashesTab extends StatefulWidget {
  const RumCrashesTab({
    super.key,
    required this.session,
    required this.sections,
    required this.app,
  });

  final SessionController session;
  final Sections sections;
  final String app;

  @override
  State<RumCrashesTab> createState() => _RumCrashesTabState();
}

class _RumCrashesTabState extends State<RumCrashesTab> {
  late final RumCrashesController _c = widget.sections.rumCrashes(widget.app);

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => _c.refresh());
  }

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return ListenableBuilder(
      listenable: _c,
      builder: (context, _) => ListScreen<RumCrash>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        emptyTitle: _c.serverTooOld ? l.rumServerTooOld : l.rumCrashesEmpty,
        itemBuilder: (context, i) => _CrashCard(crash: _c.items[i]),
      ),
    );
  }
}

/// A kind this build has not heard of is shown as it came.
String crashKindLabel(L l, String kind) => switch (kind) {
  'uncaught_exception' => l.rumCrashKindUncaught,
  'signal' => l.rumCrashKindSignal,
  'anr' => l.rumCrashKindAnr,
  'out_of_memory' => l.rumCrashKindOom,
  _ => kind,
};

class _CrashCard extends StatelessWidget {
  const _CrashCard({required this.crash});

  final RumCrash crash;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final language = Localizations.localeOf(context).languageCode;
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final c = crash;
    final frame = c.frame ?? '';
    final missing = c.missingSymbols ?? const <String>[];

    return Card(
      key: Key('rum-crash-${c.groupId}:$frame'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Expanded(
                  child: Text(
                    c.type,
                    style: mono(
                      theme.textTheme.bodyMedium,
                    )?.copyWith(fontWeight: FontWeight.w600),
                  ),
                ),
                const SizedBox(width: 8),
                Text(
                  formatCount(c.count, language),
                  style: theme.textTheme.titleSmall?.copyWith(
                    fontWeight: FontWeight.w600,
                  ),
                ),
              ],
            ),
            Text(crashKindLabel(l, c.kind), style: muted),
            if (c.message.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(top: 6),
                child: Text(c.message, style: theme.textTheme.bodyMedium),
              ),
            const SizedBox(height: 6),
            Text(
              l.rumCrashesSummary(
                formatCount(c.sessions.toDouble(), language),
                c.lastVersion.isEmpty ? l.rumReleaseNoVersion : c.lastVersion,
                relativeTimeOf(l, c.lastSeen),
              ),
              style: muted,
            ),
            if (frame.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(top: 6),
                child: Text(
                  frame,
                  key: const Key('rum-crash-frame'),
                  style: mono(theme.textTheme.bodySmall),
                ),
              ),
            if (missing.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(top: 6),
                child: Text(
                  l.rumCrashesMissingSymbols(missing.join(', ')),
                  key: const Key('rum-crash-missing-symbols'),
                  style: muted,
                ),
              ),
            if (c.stack.isNotEmpty)
              ExpansionTile(
                key: const Key('rum-crash-stack'),
                tilePadding: EdgeInsets.zero,
                childrenPadding: const EdgeInsets.only(bottom: 4),
                dense: true,
                title: Text(l.rumCrashesStack, style: muted),
                children: [
                  // A stack keeps its lines: it scrolls sideways rather than
                  // wrapping a frame into something that reads as two.
                  SingleChildScrollView(
                    scrollDirection: Axis.horizontal,
                    child: Text(
                      c.stack,
                      style: mono(theme.textTheme.bodySmall),
                    ),
                  ),
                ],
              ),
          ],
        ),
      ),
    );
  }
}
