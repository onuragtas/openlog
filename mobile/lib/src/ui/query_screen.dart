// The query console: write OQL, run it, read what came back.
//
// The one screen in this app where the person asks the question rather than
// picking from what it offers, which is why the web puts it next to the
// dashboards it is used to build.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../query.dart';
import '../session.dart';
import 'failure_text.dart';
import 'oql_view.dart';

class QueryBody extends StatefulWidget {
  const QueryBody({super.key, required this.session, required this.query});

  final SessionController session;
  final QueryController query;

  @override
  State<QueryBody> createState() => _QueryBodyState();
}

class _QueryBodyState extends State<QueryBody> {
  final _text = TextEditingController();

  @override
  void dispose() {
    _text.dispose();
    super.dispose();
  }

  void _run() => widget.query.run(_text.text);

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.query;
    final theme = Theme.of(context);

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => ListView(
        padding: const EdgeInsets.fromLTRB(12, 12, 12, 32),
        children: [
          TextField(
            key: const Key('query-text'),
            controller: _text,
            minLines: 2,
            maxLines: 6,
            // Enter inserts a newline rather than running: OQL wraps over
            // several lines and a keyboard that submits on return would make
            // the common case the hard one.
            keyboardType: TextInputType.multiline,
            textInputAction: TextInputAction.newline,
            autocorrect: false,
            enableSuggestions: false,
            decoration: InputDecoration(
              hintText: l.queryHint,
              border: const OutlineInputBorder(),
              isDense: true,
            ),
          ),
          const SizedBox(height: 10),
          Align(
            alignment: Alignment.centerRight,
            child: FilledButton(
              key: const Key('query-run'),
              onPressed: c.running ? null : _run,
              child: c.running
                  ? const SizedBox(
                      width: 16,
                      height: 16,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    )
                  : Text(l.queryRun),
            ),
          ),
          FailureBanner(
            failure: c.failure,
            baseUrl: widget.session.baseUrl ?? '',
          ),
          if (c.result == null && c.failure == null && !c.running) ...[
            const SizedBox(height: 40),
            Text(
              l.queryEmpty,
              textAlign: TextAlign.center,
              style: theme.textTheme.bodyMedium?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          ],
          if (c.result != null) ...[
            const SizedBox(height: 18),
            Card(
              child: Padding(
                padding: const EdgeInsets.all(14),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    // Which query this answers. The box moves on as soon as
                    // the person types, so without this the card would be an
                    // answer to a question no longer on screen.
                    Text(
                      c.ran,
                      key: const Key('query-ran'),
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                    const SizedBox(height: 10),
                    OqlResultView(
                      result: c.result!,
                      id: 'query',
                      // More than a dashboard tile shows: this is the screen
                      // the person came to read, not a glance.
                      facetLimit: 20,
                    ),
                    const SizedBox(height: 12),
                    Text(
                      l.queryMeta(
                        c.result!.metadata.rowsRead,
                        c.result!.metadata.elapsedMs,
                      ),
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                    if (c.result!.metadata.truncated)
                      Text(
                        l.queryTruncated,
                        style: theme.textTheme.bodySmall?.copyWith(
                          color: theme.colorScheme.onSurfaceVariant,
                        ),
                      ),
                    for (final w in c.result!.metadata.warnings)
                      Text(
                        w,
                        style: theme.textTheme.bodySmall?.copyWith(
                          color: theme.colorScheme.error,
                        ),
                      ),
                  ],
                ),
              ),
            ),
          ],
          if (c.history.isNotEmpty) ...[
            const SizedBox(height: 24),
            Text(
              l.queryRecent,
              style: theme.textTheme.titleSmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
            for (final q in c.history)
              ListTile(
                key: Key('history-${c.history.indexOf(q)}'),
                dense: true,
                contentPadding: EdgeInsets.zero,
                title: Text(
                  q,
                  maxLines: 2,
                  overflow: TextOverflow.ellipsis,
                  style: theme.textTheme.bodySmall,
                ),
                // Puts it back in the box rather than running it: a query that
                // was expensive once should not run again because a thumb
                // brushed the list.
                onTap: () => _text.text = q,
              ),
          ],
        ],
      ),
    );
  }
}
