// One error group: what it is, what has been decided about it, and what can
// be done about it from here.
//
// The actions are the reason this screen exists. An inbox that can only be
// read is a list of things to do somewhere else, and "somewhere else" at
// three in the morning is a laptop nobody opened.
import 'dart:convert';

import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../errors.dart';
import '../sections.dart';
import '../session.dart';
import 'errors_screen.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'severity.dart';
import 'trace_screen.dart';

class ErrorGroupScreen extends StatefulWidget {
  const ErrorGroupScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.group,
  });

  final SessionController session;
  final Sections sections;
  final ApmErrorGroup group;

  @override
  State<ErrorGroupScreen> createState() => _ErrorGroupScreenState();
}

class _ErrorGroupScreenState extends State<ErrorGroupScreen> {
  late final ErrorGroupController _group;
  final _comment = TextEditingController();

  @override
  void initState() {
    super.initState();
    _group = widget.sections.errorGroup(widget.group)
      // Who is signed in decides which comments show a delete button. An
      // admin may delete anyone's, but the role alone cannot be checked
      // here without a request, and a button that 403s is worse than none.
      ..myUserId = widget.session.me?.user?.id ?? '';
    WidgetsBinding.instance.addPostFrameCallback((_) => _group.load());
  }

  @override
  void dispose() {
    _comment.dispose();
    _group.dispose();
    super.dispose();
  }

  /// Runs a workflow change through the inbox controller, so the list behind
  /// this screen is the thing that reloads -- and then shows the group it
  /// came back with, rather than the one that was tapped.
  Future<void> _change(String status, {String? version}) async {
    final inbox = widget.sections.errors;
    await inbox.setStatus(_group.group.groupId, status, version: version);
    if (!mounted) return;
    for (final g in inbox.items) {
      if (g.groupId == _group.group.groupId) {
        setState(() => _group.group = g);
        return;
      }
    }
    // Gone from the list because the status filter no longer matches it.
    // Nothing to show that is newer, so the screen keeps what it had.
    setState(() {});
  }

  Future<void> _resolveInVersion() async {
    final l = L.of(context);
    final controller = TextEditingController(
      text: _group.group.resolvedInVersion,
    );
    final version = await showDialog<String>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text(l.errorsResolveInVersion),
        content: TextField(
          key: const Key('error-version'),
          controller: controller,
          autofocus: true,
          decoration: InputDecoration(
            labelText: l.errorsVersion,
            helperText: l.errorsVersionHint,
            helperMaxLines: 3,
            border: const OutlineInputBorder(),
          ),
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.of(context).pop(),
            child: Text(l.ruleCancel),
          ),
          FilledButton(
            key: const Key('error-version-ok'),
            onPressed: () => Navigator.of(context).pop(controller.text.trim()),
            child: Text(l.errorsResolve),
          ),
        ],
      ),
    );
    controller.dispose();
    if (version == null || version.isEmpty) return;
    await _change('resolved', version: version);
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final inbox = widget.sections.errors;

    return Scaffold(
      appBar: AppBar(title: Text(widget.group.errorType)),
      body: ListenableBuilder(
        listenable: Listenable.merge([_group, inbox]),
        builder: (context, _) {
          final g = _group.group;
          final busy = _group.busy || inbox.busy == g.groupId;
          return ListView(
            padding: const EdgeInsets.fromLTRB(16, 14, 16, 28),
            children: [
              Text(g.message, style: theme.textTheme.bodyMedium),
              const SizedBox(height: 10),
              Wrap(
                spacing: 8,
                runSpacing: 4,
                crossAxisAlignment: WrapCrossAlignment.center,
                children: [
                  Text(g.serviceName, style: theme.textTheme.bodySmall),
                  if (g.environment.isNotEmpty)
                    Text(g.environment, style: theme.textTheme.bodySmall),
                  ...errorStatusTags(l, g),
                ],
              ),
              const SizedBox(height: 8),
              _Facts(group: g),
              FailureBanner(
                failure: _group.failure ?? inbox.failure,
                baseUrl: widget.session.baseUrl ?? '',
              ),
              if (g.lastTraceId.isNotEmpty)
                Align(
                  alignment: Alignment.centerLeft,
                  child: TextButton.icon(
                    key: const Key('error-trace'),
                    onPressed: () => Navigator.of(context).push(
                      MaterialPageRoute<void>(
                        builder: (_) => TraceScreen(
                          session: widget.session,
                          sections: widget.sections,
                          traceId: g.lastTraceId,
                        ),
                      ),
                    ),
                    icon: const Icon(Icons.route_outlined),
                    label: Text(l.errorsLastTrace),
                  ),
                ),
              if (!inbox.workflow)
                Padding(
                  padding: const EdgeInsets.only(top: 8),
                  child: Text(
                    l.errorsNoWorkflow,
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
                )
              else ...[
                const SizedBox(height: 6),
                Wrap(
                  spacing: 8,
                  runSpacing: 4,
                  children: [
                    if (g.status != ApmErrorStatus.resolved)
                      OutlinedButton(
                        key: const Key('error-resolve'),
                        onPressed: busy ? null : () => _change('resolved'),
                        child: Text(l.errorsResolve),
                      ),
                    OutlinedButton(
                      key: const Key('error-resolve-version'),
                      onPressed: busy ? null : _resolveInVersion,
                      child: Text(l.errorsResolveInVersion),
                    ),
                    if (g.status != ApmErrorStatus.ignored)
                      OutlinedButton(
                        key: const Key('error-ignore'),
                        onPressed: busy ? null : () => _change('ignored'),
                        child: Text(l.errorsIgnore),
                      ),
                    if (g.status != ApmErrorStatus.unresolved)
                      OutlinedButton(
                        key: const Key('error-reopen'),
                        onPressed: busy ? null : () => _change('unresolved'),
                        child: Text(l.errorsReopen),
                      ),
                  ],
                ),
                _Detail(
                  detail: _group.detail,
                  session: widget.session,
                  sections: widget.sections,
                ),
                const SizedBox(height: 18),
                Text(l.errorsComments, style: theme.textTheme.titleSmall),
                if (_group.loading)
                  const Padding(
                    padding: EdgeInsets.symmetric(vertical: 16),
                    child: Center(child: CircularProgressIndicator()),
                  )
                else if (_group.comments.isEmpty)
                  Padding(
                    padding: const EdgeInsets.symmetric(vertical: 8),
                    child: Text(
                      l.errorsNoComments,
                      key: const Key('error-no-comments'),
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                  )
                else
                  for (final c in _group.comments)
                    _Comment(
                      comment: c,
                      canDelete: _group.canDelete(c),
                      busy: _group.busy,
                      onDelete: () => _group.deleteComment(c.id),
                    ),
                _Activity(detail: _group.detail),
                const SizedBox(height: 8),
                TextField(
                  key: const Key('error-comment'),
                  controller: _comment,
                  minLines: 2,
                  maxLines: 5,
                  onChanged: (_) => setState(() {}),
                  decoration: InputDecoration(
                    labelText: l.errorsComment,
                    // The server counts bytes, not characters, and a
                    // Turkish sentence is longer in bytes than it looks.
                    errorText:
                        utf8.encode(_comment.text).length > commentMaxBytes
                        ? l.errorsCommentTooLong
                        : null,
                    border: const OutlineInputBorder(),
                    isDense: true,
                  ),
                ),
                const SizedBox(height: 8),
                Align(
                  alignment: Alignment.centerRight,
                  child: FilledButton(
                    key: const Key('error-comment-send'),
                    onPressed:
                        _group.busy ||
                            _comment.text.trim().isEmpty ||
                            utf8.encode(_comment.text).length > commentMaxBytes
                        ? null
                        : () async {
                            await _group.comment(_comment.text.trim());
                            if (_group.failure == null) _comment.clear();
                          },
                    child: Text(l.errorsSend),
                  ),
                ),
              ],
            ],
          );
        },
      ),
    );
  }
}

class _Facts extends StatelessWidget {
  const _Facts({required this.group});

  final ApmErrorGroup group;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final style = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          l.errorsCountOfTotal(group.count.round(), group.totalCount.round()),
          style: style,
        ),
        if (group.firstSeen != null)
          Text(
            l.errorsFirstSeen(relativeTimeOf(l, group.firstSeen!)),
            style: style,
          ),
        if (group.lastSeen != null)
          Text(
            l.errorsLastSeen(relativeTimeOf(l, group.lastSeen!)),
            style: style,
          ),
        if (group.resolvedInVersion.isNotEmpty)
          Text(l.errorsResolvedIn(group.resolvedInVersion), style: style),
        if (group.regressionCount > 0)
          Text(
            l.errorsRegressions(group.regressionCount),
            style: theme.textTheme.bodySmall?.copyWith(
              color: severityTextColor(context, SeverityLevel.critical),
            ),
          ),
      ],
    );
  }
}

class _Comment extends StatelessWidget {
  const _Comment({
    required this.comment,
    required this.canDelete,
    required this.busy,
    required this.onDelete,
  });

  final ApmErrorComment comment;
  final bool canDelete;
  final bool busy;
  final VoidCallback onDelete;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return Padding(
      key: Key('comment-${comment.id}'),
      padding: const EdgeInsets.only(top: 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  comment.authorName.isEmpty
                      ? comment.authorEmail
                      : comment.authorName,
                  style: theme.textTheme.bodySmall?.copyWith(
                    fontWeight: FontWeight.w600,
                  ),
                ),
              ),
              Text(
                relativeTimeOf(l, comment.createdAt),
                style: theme.textTheme.bodySmall?.copyWith(
                  color: theme.colorScheme.onSurfaceVariant,
                ),
              ),
              if (canDelete)
                IconButton(
                  key: Key('comment-delete-${comment.id}'),
                  tooltip: l.errorsDeleteComment,
                  onPressed: busy ? null : onDelete,
                  visualDensity: VisualDensity.compact,
                  icon: const Icon(Icons.delete_outline, size: 18),
                ),
            ],
          ),
          Text(comment.body, style: theme.textTheme.bodyMedium),
        ],
      ),
    );
  }
}

/// The parts of the group that only the detail endpoint carries: what it
/// threw, who it hit, and the requests it happened in.
class _Detail extends StatelessWidget {
  const _Detail({
    required this.detail,
    required this.session,
    required this.sections,
  });

  final ApmErrorGroupDetail? detail;
  final SessionController session;
  final Sections sections;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final d = detail;
    if (d == null) return const SizedBox.shrink();
    final affected = d.affected;

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        if (d.lastMessage.isNotEmpty && d.lastMessage != d.message) ...[
          const SizedBox(height: 14),
          Text(l.errorsLastMessage, style: theme.textTheme.titleSmall),
          Text(d.lastMessage, style: theme.textTheme.bodySmall),
        ],
        const SizedBox(height: 14),
        Text(l.errorsStacktrace, style: theme.textTheme.titleSmall),
        if (d.stacktrace.isEmpty)
          Text(
            l.errorsNoStack,
            key: const Key('error-no-stack'),
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          )
        else
          // Scrolls sideways rather than wrapping: a stack frame broken
          // across two lines stops looking like a stack frame.
          SingleChildScrollView(
            scrollDirection: Axis.horizontal,
            child: Text(
              d.stacktrace,
              key: const Key('error-stack'),
              style: theme.textTheme.bodySmall?.copyWith(
                fontFeatures: const [FontFeature.tabularFigures()],
              ),
            ),
          ),
        if (d.symbolicatedFrames > 0)
          Text(
            l.errorsSymbolicated(d.symbolicatedFrames),
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
        if (affected.versions.isNotEmpty ||
            affected.hosts.isNotEmpty ||
            affected.containers.isNotEmpty ||
            affected.transactions.isNotEmpty) ...[
          const SizedBox(height: 14),
          Text(l.errorsAffected, style: theme.textTheme.titleSmall),
          _AffectedList(
            title: l.errorsAffectedVersions,
            items: affected.versions,
          ),
          _AffectedList(title: l.errorsAffectedHosts, items: affected.hosts),
          _AffectedList(
            title: l.errorsAffectedContainers,
            items: affected.containers,
          ),
          _AffectedList(
            title: l.errorsAffectedTransactions,
            items: affected.transactions,
          ),
        ],
        const SizedBox(height: 14),
        Text(l.errorsSamples, style: theme.textTheme.titleSmall),
        if (d.samples.isEmpty)
          Text(
            l.errorsNoSamples,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          )
        else
          for (final sample in d.samples.take(5))
            ListTile(
              key: Key('error-sample-${sample.spanId}'),
              contentPadding: EdgeInsets.zero,
              visualDensity: VisualDensity.compact,
              title: Text(
                sample.transactionName.isEmpty
                    ? sample.spanName
                    : sample.transactionName,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: theme.textTheme.bodyMedium,
              ),
              subtitle: Text(
                [
                  relativeTimeOf(l, sample.timestamp),
                  '${sample.durationMs.toStringAsFixed(sample.durationMs >= 100 ? 0 : 1)} ms',
                  if (sample.version.isNotEmpty) 'v${sample.version}',
                ].join(' · '),
                style: theme.textTheme.bodySmall,
              ),
              trailing: const Icon(Icons.chevron_right, size: 18),
              onTap: () => Navigator.of(context).push(
                MaterialPageRoute<void>(
                  builder: (_) => TraceScreen(
                    session: session,
                    sections: sections,
                    traceId: sample.traceId,
                  ),
                ),
              ),
            ),
      ],
    );
  }
}

class _AffectedList extends StatelessWidget {
  const _AffectedList({required this.title, required this.items});

  final String title;
  final List<ApmAffected> items;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    if (items.isEmpty) return const SizedBox.shrink();
    return Padding(
      padding: const EdgeInsets.only(top: 6),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            title,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
          // The count next to each one: "every version" and "one version"
          // are different answers to "what broke".
          Text(
            [
              for (final a in items.take(6))
                '${a.name.isEmpty ? (a.value.isEmpty ? '–' : a.value) : a.name}'
                    ' (${a.count.round()})',
            ].join(', '),
            style: theme.textTheme.bodySmall,
          ),
        ],
      ),
    );
  }
}

/// What people did to this group, newest last, as the server recorded it.
class _Activity extends StatelessWidget {
  const _Activity({required this.detail});

  final ApmErrorGroupDetail? detail;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final activity = detail?.activity ?? const <ApmErrorActivity>[];
    if (activity.isEmpty) return const SizedBox.shrink();

    return Padding(
      padding: const EdgeInsets.only(top: 14),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(l.errorsActivity, style: theme.textTheme.titleSmall),
          for (final a in activity)
            Text(
              // The action as the server names it: inventing friendlier
              // words for `apm.error_group.regressed` would mean guessing
              // which ones exist.
              '${relativeTimeOf(l, a.createdAt)} · ${a.actorEmail.isEmpty ? '—' : a.actorEmail} · ${a.action}',
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
        ],
      ),
    );
  }
}
