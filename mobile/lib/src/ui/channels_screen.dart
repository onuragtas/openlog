// The notification channels, and the one thing worth doing to them from a
// phone: finding out whether they still reach anyone.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../sections.dart';
import '../session.dart';
import 'deliveries_screen.dart';
import 'list_scaffold.dart';
import 'sections_screen.dart';
import 'severity.dart';

class ChannelsBody extends StatelessWidget {
  const ChannelsBody({
    super.key,
    required this.session,
    required this.sections,
    required this.active,
  });

  final SessionController session;
  final Sections sections;
  final bool active;

  @override
  Widget build(BuildContext context) {
    final c = sections.channels;

    return SectionBody<AlertChannel>(
      session: session,
      controller: c,
      searchKey: 'channels-search',
      searchHint: (l) => l.channelsSearch,
      active: active,
      emptyTitle: (l) =>
          // An empty list means two different things, and the difference
          // matters: nobody made a channel, or this installation cannot
          // store one at all.
          c.secretsConfigured ? l.channelsEmpty : l.channelsNoSecrets,
      header: ListenableBuilder(
        listenable: c,
        builder: (context, _) {
          final l = L.of(context);
          final theme = Theme.of(context);
          return Padding(
            padding: const EdgeInsets.only(bottom: 8, left: 4),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  c.secretsConfigured
                      ? l.channelEditOnWeb
                      : l.channelsNoSecrets,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: c.secretsConfigured
                        ? theme.colorScheme.onSurfaceVariant
                        : severityTextColor(context, SeverityLevel.warning),
                  ),
                ),
                Align(
                  alignment: Alignment.centerLeft,
                  child: TextButton.icon(
                    key: const Key('channels-deliveries'),
                    onPressed: () => openDeliveries(
                      context,
                      session: session,
                      sections: sections,
                    ),
                    icon: const Icon(Icons.outbox),
                    label: Text(l.deliveriesOpen),
                  ),
                ),
              ],
            ),
          );
        },
      ),
      card: (context, channel) => ListenableBuilder(
        listenable: c,
        builder: (context, _) => _ChannelCard(
          channel: channel,
          busy: c.testing == channel.id,
          result: c.results[channel.id],
          canTest: c.secretsConfigured,
          onTest: () => c.test(channel.id),
          onDeliveries: () => openDeliveries(
            context,
            session: session,
            sections: sections,
            channelId: channel.id,
            channelName: channel.name,
          ),
        ),
      ),
    );
  }
}

class _ChannelCard extends StatelessWidget {
  const _ChannelCard({
    required this.channel,
    required this.busy,
    required this.result,
    required this.canTest,
    required this.onTest,
    required this.onDeliveries,
  });

  final AlertChannel channel;
  final bool busy;
  final AlertChannelTestResult? result;
  final bool canTest;
  final VoidCallback onTest;

  /// This channel's own log, which is the answer to "did it reach anyone"
  /// when the test button says it would today.
  final VoidCallback onDeliveries;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );
    final last = channel.lastDelivery;

    return Card(
      key: Key('channel-${channel.id}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(
                  child: Text(
                    channel.name,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: theme.textTheme.titleSmall,
                  ),
                ),
                const SizedBox(width: 8),
                Text(channel.type.wire, style: muted),
              ],
            ),
            const SizedBox(height: 6),
            Wrap(
              spacing: 8,
              runSpacing: 4,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [
                if (!channel.enabled)
                  Tag(label: l.channelOff, level: SeverityLevel.unknown),
                // The masked hint is how a person recognises which webhook
                // this is without the secret ever leaving the server.
                for (final hint in channel.secretHints.values.take(1))
                  Text(hint, style: muted),
                if (last != null)
                  Text(
                    l.channelLastDelivery(relativeTimeOf(l, last.at)),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: last.status == AlertNotificationStatus.failed
                          ? severityTextColor(context, SeverityLevel.critical)
                          : theme.colorScheme.onSurfaceVariant,
                    ),
                  ),
              ],
            ),
            if (last != null && last.error.isNotEmpty)
              Padding(
                padding: const EdgeInsets.only(top: 4),
                child: Text(
                  last.error,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.error,
                  ),
                ),
              ),
            if (result != null) _Result(result: result!),
            Row(
              mainAxisAlignment: MainAxisAlignment.end,
              children: [
                TextButton(
                  key: Key('channel-deliveries-${channel.id}'),
                  onPressed: onDeliveries,
                  child: Text(l.deliveriesOpen),
                ),
                TextButton(
                  key: Key('channel-test-${channel.id}'),
                  onPressed: busy || !canTest ? null : onTest,
                  child: busy
                      ? const SizedBox(
                          width: 16,
                          height: 16,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        )
                      : Text(l.channelTest),
                ),
              ],
            ),
          ],
        ),
      ),
    );
  }
}

/// What the test said.
///
/// Read from the body, never from the status code: the server answers 200
/// with `success: false` when the receiver refused, and "sent" over that
/// would tell someone their pager works when it does not.
class _Result extends StatelessWidget {
  const _Result({required this.result});

  final AlertChannelTestResult result;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    final text = result.success
        ? l.channelTestOk(result.durationMs)
        // A status code of 0 means nothing answered at all, so there is no
        // code worth printing -- just what went wrong.
        : result.statusCode > 0
        ? l.channelTestFailedCode(result.statusCode, result.error)
        : l.channelTestFailed(result.error);

    return Padding(
      padding: const EdgeInsets.only(top: 6),
      child: Text(
        text,
        key: const Key('channel-result'),
        style: theme.textTheme.bodySmall?.copyWith(
          color: severityTextColor(
            context,
            result.success ? SeverityLevel.good : SeverityLevel.critical,
          ),
        ),
      ),
    );
  }
}
