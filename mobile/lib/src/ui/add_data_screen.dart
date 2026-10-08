// Add data: where to point something at this server.
//
// The web's catalogue of data sources and their install commands is a page's
// worth of snippets nobody is going to run from a phone. What a phone is good
// for is the thing you look up while standing at a terminal: the endpoint, and
// which release the commands pin. That is what this screen is.
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'severity.dart';

class AddDataBody extends StatefulWidget {
  const AddDataBody({
    super.key,
    required this.session,
    required this.onboarding,
    required this.active,
  });

  final SessionController session;
  final OnboardingController onboarding;
  final bool active;

  @override
  State<AddDataBody> createState() => _AddDataBodyState();
}

class _AddDataBodyState extends State<AddDataBody> {
  @override
  void initState() {
    super.initState();
    _loadIfVisible();
  }

  @override
  void didUpdateWidget(AddDataBody old) {
    super.didUpdateWidget(old);
    _loadIfVisible();
  }

  void _loadIfVisible() {
    final c = widget.onboarding;
    if (!widget.active || c.loaded || c.loadingFirst) return;
    WidgetsBinding.instance.addPostFrameCallback((_) => c.refresh());
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);

    return ListenableBuilder(
      listenable: widget.onboarding,
      builder: (context, _) => DetailBody<Onboarding>(
        controller: widget.onboarding,
        baseUrl: widget.session.baseUrl ?? '',
        builder: (context, o) => _body(context, l, o),
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, Onboarding o) {
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return [
      _Endpoint(label: l.addDataOtlpHttp, endpoint: o.otlpHttp),
      _Endpoint(label: l.addDataOtlpGrpc, endpoint: o.otlpGrpc),

      DetailSection(
        title: l.addDataVersions,
        children: [
          Text(
            l.addDataServer(o.serverVersion),
            style: theme.textTheme.bodyMedium,
          ),
          if (o.agentVersion == null)
            Text(l.addDataAgentDev, style: muted)
          else
            Text(
              l.addDataAgent(o.agentVersion!, o.releaseChannel.wire),
              style: muted,
            ),
        ],
      ),

      DetailSection(
        title: l.addDataBrowser,
        children: [
          if (o.corsEnabled)
            Text(l.addDataCorsOn(o.corsAllowedOrigins.join(', ')), style: muted)
          else
            // Not an error, but the one thing that silently stops browser
            // data from ever arriving, so it is worth saying out loud.
            Text(
              l.addDataCorsOff,
              style: theme.textTheme.bodySmall?.copyWith(
                color: severityTextColor(context, SeverityLevel.warning),
              ),
            ),
        ],
      ),

      const SizedBox(height: 22),
      Text(l.addDataSourcesOnWeb, style: muted),
    ];
  }
}

/// An endpoint with a copy button. Copying is the point: this is a value that
/// has to end up in a config file somewhere else.
class _Endpoint extends StatelessWidget {
  const _Endpoint({required this.label, required this.endpoint});

  final String label;
  final OnboardingEndpoint endpoint;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return DetailSection(
      title: label,
      children: [
        Row(
          children: [
            Expanded(
              child: SelectableText(
                endpoint.url,
                style: theme.textTheme.bodyMedium,
              ),
            ),
            IconButton(
              key: Key('copy-${endpoint.url}'),
              tooltip: l.addDataCopied,
              icon: const Icon(Icons.copy_outlined, size: 18),
              onPressed: () async {
                await Clipboard.setData(ClipboardData(text: endpoint.url));
                if (!context.mounted) return;
                ScaffoldMessenger.of(context).showSnackBar(
                  SnackBar(
                    content: Text(l.addDataCopied),
                    duration: const Duration(seconds: 2),
                  ),
                );
              },
            ),
          ],
        ),
        // A derived endpoint is this server's guess from the request it
        // answered; it can be right and still be unreachable from where the
        // agent runs, which is a failure nobody connects to this screen.
        if (endpoint.source != OnboardingEndpointSource.configured)
          Text(
            l.addDataEndpointDerived,
            style: theme.textTheme.bodySmall?.copyWith(
              color: severityTextColor(context, SeverityLevel.warning),
            ),
          ),
      ],
    );
  }
}
