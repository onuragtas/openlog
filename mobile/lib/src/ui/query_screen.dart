// The query console: write OQL, run it, read what came back.
//
// The one screen in this app where the person asks the question rather than
// picking from what it offers, which is why the web puts it next to the
// dashboards it is used to build.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../oql.dart';
import '../query.dart';
import '../sections.dart';
import '../session.dart';
import 'failure_text.dart';
import 'oql_view.dart';
import 'oql_wizard.dart';

/// The web's own examples, with the same plain-language titles: a list that
/// says what a query answers, not only how it is written.
const _queryExamples = [
  (
    id: 'logsBySeverity',
    query: 'SELECT count(*) FROM Log FACET severity TIMESERIES',
  ),
  (
    id: 'errorsByService',
    query:
        "SELECT count(*) FROM Log WHERE severity = 'ERROR' FACET service.name",
  ),
  (
    id: 'slowTransactions',
    query:
        'SELECT percentile(duration.ms, 50, 95), count(*) FROM Transaction '
        'FACET transaction.name LIMIT 5',
  ),
  (
    id: 'cpuByHost',
    query:
        "SELECT average(value) FROM Metric WHERE metricName = "
        "'system.cpu.utilization' FACET host.name TIMESERIES AUTO",
  ),
  (
    id: 'durationHistogram',
    query: 'SELECT histogram(duration.ms, 1000, 20) FROM Transaction',
  ),
  (
    id: 'trafficVsYesterday',
    query: 'SELECT count(*) FROM Transaction TIMESERIES COMPARE WITH 1 day ago',
  ),
];

String _exampleTitle(L l, String id) => switch (id) {
  'logsBySeverity' => l.queryExampleLogsBySeverity,
  'errorsByService' => l.queryExampleErrorsByService,
  'slowTransactions' => l.queryExampleSlowTransactions,
  'cpuByHost' => l.queryExampleCpuByHost,
  'durationHistogram' => l.queryExampleDurationHistogram,
  _ => l.queryExampleTrafficVsYesterday,
};

class QueryBody extends StatefulWidget {
  const QueryBody({
    super.key,
    required this.session,
    required this.sections,
    required this.query,
  });

  final SessionController session;

  /// The language's own schema and the field dictionary, both of which the
  /// guided builder needs.
  final Sections sections;
  final QueryController query;

  @override
  State<QueryBody> createState() => _QueryBodyState();
}

class _QueryBodyState extends State<QueryBody> {
  final _text = TextEditingController();

  /// Somebody arriving with nothing starts in the builder, as on the web;
  /// once there is a query on screen it is the query that matters.
  bool _wizard = true;
  bool _examples = false;

  @override
  void initState() {
    super.initState();
    // Validated while typing, as the web's editor does: the server is the
    // one that parses OQL, and it answers this without reading telemetry.
    _text.addListener(() => widget.sections.oqlValidation.schedule(_text.text));
  }

  @override
  void dispose() {
    _text.dispose();
    super.dispose();
  }

  void _run([String? query]) {
    if (query != null) {
      _text.text = query;
      setState(() {
        _wizard = false;
        _examples = false;
      });
    }
    widget.query.run(_text.text);
  }

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
          // The two ways in, as the web offers them above its editor: build
          // the query by picking, or start from one that already works.
          Wrap(
            spacing: 8,
            runSpacing: 8,
            children: [
              OutlinedButton.icon(
                key: const Key('query-wizard-toggle'),
                onPressed: () => setState(() {
                  _wizard = !_wizard;
                  if (_wizard) _examples = false;
                }),
                icon: Icon(
                  Icons.auto_fix_high_outlined,
                  size: 18,
                  color: _wizard ? theme.colorScheme.primary : null,
                ),
                label: Text(l.wizardTitle),
              ),
              OutlinedButton.icon(
                key: const Key('query-examples-toggle'),
                onPressed: () => setState(() {
                  _examples = !_examples;
                  if (_examples) _wizard = false;
                }),
                icon: Icon(
                  Icons.lightbulb_outline,
                  size: 18,
                  color: _examples ? theme.colorScheme.primary : null,
                ),
                label: Text(l.queryExamples),
              ),
            ],
          ),
          if (_wizard) ...[
            const SizedBox(height: 10),
            Card(
              child: Padding(
                padding: const EdgeInsets.all(12),
                child: OqlWizard(
                  key: const Key('query-wizard'),
                  session: widget.session,
                  schema: widget.sections.oqlSchema,
                  fields: widget.sections.fields,
                  onRun: _run,
                ),
              ),
            ),
          ],
          if (_examples) ...[
            const SizedBox(height: 10),
            Text(
              l.queryExamplesHint,
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
            for (final e in _queryExamples)
              ListTile(
                key: Key('example-${e.id}'),
                dense: true,
                contentPadding: EdgeInsets.zero,
                title: Text(_exampleTitle(l, e.id)),
                subtitle: Text(
                  e.query,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.onSurfaceVariant,
                    fontFeatures: const [FontFeature.tabularFigures()],
                  ),
                ),
                onTap: () => _run(e.query),
              ),
          ],
          const SizedBox(height: 10),
          ListenableBuilder(
            // The box is drawn against the validation as well as its own
            // text: the web underlines the mistake, and the least a box
            // without an editor can do is say that there is one.
            listenable: Listenable.merge([
              widget.sections.oqlValidation,
              _text,
            ]),
            builder: (context, _) {
              final bad = widget.sections.oqlValidation
                  .diagnosticsFor(_text.text)
                  .any((p) => p.error);
              return TextField(
                key: const Key('query-text'),
                controller: _text,
                minLines: 2,
                maxLines: 6,
                // Enter inserts a newline rather than running: OQL wraps
                // over several lines and a keyboard that submits on return
                // would make the common case the hard one.
                keyboardType: TextInputType.multiline,
                textInputAction: TextInputAction.newline,
                autocorrect: false,
                enableSuggestions: false,
                decoration: InputDecoration(
                  hintText: l.queryHint,
                  border: const OutlineInputBorder(),
                  isDense: true,
                  enabledBorder: bad
                      ? OutlineInputBorder(
                          borderSide: BorderSide(
                            color: theme.colorScheme.error,
                          ),
                        )
                      : null,
                  focusedBorder: bad
                      ? OutlineInputBorder(
                          borderSide: BorderSide(
                            color: theme.colorScheme.error,
                            width: 2,
                          ),
                        )
                      : null,
                ),
              );
            },
          ),
          _Problems(validation: widget.sections.oqlValidation, text: _text),
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

/// What the server says is wrong with the text in the box, while it is
/// being typed.
///
/// Listens to the box as well as to the controller: a problem list under a
/// query that has already been edited would point at columns that moved.
class _Problems extends StatelessWidget {
  const _Problems({required this.validation, required this.text});

  final OqlValidationController validation;
  final TextEditingController text;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return ListenableBuilder(
      listenable: Listenable.merge([validation, text]),
      builder: (context, _) {
        final problems = validation.diagnosticsFor(text.text);
        if (problems.isEmpty) return const SizedBox.shrink();
        return Padding(
          padding: const EdgeInsets.only(top: 6),
          child: Column(
            key: const Key('query-problems'),
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                l.queryProblems,
                style: theme.textTheme.labelSmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
              for (final p in problems)
                Text(
                  '${p.error ? l.queryError : l.queryWarning}: '
                  '${l.queryProblem(p.diagnostic.line, p.diagnostic.column, p.diagnostic.message)}',
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: p.error
                        ? theme.colorScheme.error
                        : theme.colorScheme.tertiary,
                  ),
                ),
            ],
          ),
        );
      },
    );
  }
}
