// What changed in the organization, newest first.
//
// The filters go to the server, because the log is longer than any page:
// filtering what already arrived would only narrow one page of it. The
// cursor belongs to the filters it was made with, so changing one starts
// over.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../audit.dart';
import '../session.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';

class AuditTab extends StatefulWidget {
  const AuditTab({super.key, required this.session, required this.controller});

  final SessionController session;
  final AuditController controller;

  @override
  State<AuditTab> createState() => _AuditTabState();
}

class _AuditTabState extends State<AuditTab> {
  final _actor = TextEditingController();
  final _action = TextEditingController();

  @override
  void dispose() {
    _actor.dispose();
    _action.dispose();
    super.dispose();
  }

  void _apply() {
    final c = widget.controller;
    c.actor = _actor.text.trim();
    c.action = _action.text.trim();
    c.load();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final c = widget.controller;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => Column(
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 12, 16, 0),
            child: Row(
              children: [
                Expanded(
                  child: TextField(
                    key: const Key('audit-actor'),
                    controller: _actor,
                    textInputAction: TextInputAction.search,
                    onSubmitted: (_) => _apply(),
                    decoration: InputDecoration(
                      labelText: l.auditActor,
                      border: const OutlineInputBorder(),
                      isDense: true,
                    ),
                  ),
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: TextField(
                    key: const Key('audit-action'),
                    controller: _action,
                    textInputAction: TextInputAction.search,
                    onSubmitted: (_) => _apply(),
                    decoration: InputDecoration(
                      labelText: l.auditAction,
                      // A prefix, as the server reads it: `member.` finds
                      // every member change, `member.remove` only one kind.
                      helperText: l.auditActionHint,
                      helperMaxLines: 2,
                      border: const OutlineInputBorder(),
                      isDense: true,
                    ),
                  ),
                ),
                IconButton(
                  key: const Key('audit-search'),
                  tooltip: l.tracesSearch,
                  onPressed: _apply,
                  icon: const Icon(Icons.search),
                ),
              ],
            ),
          ),
          Padding(
            padding: const EdgeInsets.symmetric(horizontal: 16),
            child: FailureBanner(
              failure: c.failure,
              baseUrl: widget.session.baseUrl ?? '',
            ),
          ),
          Expanded(
            child: c.loading
                ? const Center(child: CircularProgressIndicator())
                : c.events.isEmpty
                ? Center(
                    child: Text(
                      l.auditEmpty,
                      key: const Key('audit-empty'),
                      style: theme.textTheme.bodyMedium?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                  )
                : RefreshIndicator(
                    onRefresh: c.load,
                    child: ListView.separated(
                      padding: const EdgeInsets.fromLTRB(16, 8, 16, 24),
                      itemCount: c.events.length + 1,
                      separatorBuilder: (context, _) =>
                          const Divider(height: 1),
                      itemBuilder: (context, i) {
                        if (i == c.events.length) return _more(context, l, c);
                        return _EventRow(event: c.events[i]);
                      },
                    ),
                  ),
          ),
        ],
      ),
    );
  }

  Widget _more(BuildContext context, L l, AuditController c) {
    final theme = Theme.of(context);
    if (c.nextCursor == null || c.nextCursor!.isEmpty) {
      return Padding(
        padding: const EdgeInsets.symmetric(vertical: 14),
        child: Center(
          child: Text(
            l.auditEnd,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
        ),
      );
    }
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 10),
      child: Center(
        child: c.loadingMore
            ? const SizedBox(
                width: 18,
                height: 18,
                child: CircularProgressIndicator(strokeWidth: 2),
              )
            : OutlinedButton(
                key: const Key('audit-more'),
                onPressed: c.more,
                child: Text(l.auditMore),
              ),
      ),
    );
  }
}

class _EventRow extends StatelessWidget {
  const _EventRow({required this.event});

  final AuditEvent event;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final key = event.actorApiKey;

    return Padding(
      key: Key('audit-${event.id}'),
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  // The action as the server writes it. Translating
                  // `license_key.revoke` would mean inventing a word per
                  // action and guessing which ones exist.
                  event.action,
                  style: theme.textTheme.bodyMedium,
                ),
              ),
              Text(relativeTimeOf(l, event.createdAt), style: muted),
            ],
          ),
          Text(
            [
              auditActor(event, l.auditUnknownActor),
              // Which key, when a key did it: "the key is the actor" is
              // the whole point of recording it.
              if (key != null) l.auditWithKey,
              if (event.targetType.isNotEmpty)
                '${event.targetType} ${event.targetId}'.trim(),
              if (event.ip.isNotEmpty) event.ip,
            ].join(' · '),
            style: muted,
          ),
        ],
      ),
    );
  }
}
