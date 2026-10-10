// The source maps the server keeps to un-minify browser stacks.
//
// Reading and removing, not uploading: a .map file is build output and
// lives on the machine that built it, which is never this phone. The web's
// upload form stays where the file is. Everything the list says is here.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../keys.dart';
import '../roles.dart';
import '../session.dart';
import 'keys_tabs.dart';
import 'list_scaffold.dart';

class SourceMapsTab extends StatelessWidget {
  const SourceMapsTab({
    super.key,
    required this.session,
    required this.controller,
  });

  final SessionController session;
  final SourceMapsController controller;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final manage = can(session.me?.role, 'source_maps.manage');

    return KeysTab<SourceMap>(
      session: session,
      controller: controller,
      emptyText: l.sourceMapsEmpty,
      rows: (context, maps) => [
        Padding(
          padding: const EdgeInsets.only(bottom: 6),
          child: Text(
            l.sourceMapsUploadElsewhere,
            style: Theme.of(context).textTheme.bodySmall?.copyWith(
              color: Theme.of(context).colorScheme.onSurfaceVariant,
            ),
          ),
        ),
        for (final m in maps)
          KeyRow(
            key: Key('source-map-${m.id}'),
            name: m.script,
            prefix: m.app,
            subtitle: [
              l.sourceMapsSize(_kb(m.sizeBytes)),
              relativeTimeOf(l, m.createdAt),
            ].join(' · '),
            revokedAt: null,
            busy: controller.busy == m.id,
            onRevoke: !manage
                ? null
                : () async {
                    if (await confirmRevoke(
                      context,
                      m.script,
                      l.sourceMapsDeleteBody,
                    )) {
                      await controller.remove(m.id);
                    }
                  },
          ),
      ],
      form: null,
    );
  }

  /// Kilobytes, because a source map is never small enough for bytes to
  /// mean anything and rarely large enough for megabytes.
  String _kb(int bytes) => '${(bytes / 1024).round()}';
}
