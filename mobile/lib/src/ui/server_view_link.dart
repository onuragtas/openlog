// From an application's database call to the server's view of the same
// statement. The lookup runs only when asked, and opens the statement on the
// instance where it cost the most.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../databases.dart';
import '../sections.dart';
import '../session.dart';
import 'database_screen.dart';
import 'detail_scaffold.dart';
import 'failure_text.dart';

class ServerViewLink extends StatefulWidget {
  const ServerViewLink({
    super.key,
    required this.session,
    required this.sections,
    required this.dbSystem,
    required this.statement,
  });

  final SessionController session;
  final Sections sections;
  final String dbSystem;
  final String statement;

  @override
  State<ServerViewLink> createState() => _ServerViewLinkState();
}

class _ServerViewLinkState extends State<ServerViewLink> {
  late final DbLookupController _c = widget.sections.dbLookup();

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  Future<void> _open() async {
    final match = await _c.ask(
      dbSystem: widget.dbSystem,
      statement: widget.statement,
    );
    if (match == null || !mounted) return;
    await Navigator.of(context).push(
      MaterialPageRoute<void>(
        builder: (_) => DbQueryScreen(
          session: widget.session,
          sections: widget.sections,
          instance: match.instance,
          fingerprint: match.fingerprint,
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return ListenableBuilder(
      listenable: _c,
      builder: (context, _) {
        if (_c.notOffered) {
          return const NotOfferedNote(
            padding: EdgeInsets.symmetric(vertical: 8),
          );
        }
        if (_c.notMonitored) {
          return Padding(
            padding: const EdgeInsets.symmetric(vertical: 8),
            child: Text(
              l.dbNotMonitored,
              key: const Key('db-not-monitored'),
              style: theme.textTheme.bodySmall?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          );
        }
        return Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisSize: MainAxisSize.min,
          children: [
            TextButton.icon(
              key: const Key('db-server-view'),
              onPressed: _c.loading ? null : _open,
              icon: const Icon(Icons.dns_outlined, size: 16),
              label: Text(l.dbServerView),
            ),
            FailureBanner(
              failure: _c.failure,
              baseUrl: widget.session.baseUrl ?? '',
            ),
          ],
        );
      },
    );
  }
}
