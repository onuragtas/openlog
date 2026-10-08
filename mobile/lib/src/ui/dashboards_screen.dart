// Dashboards, read-only: the list, and one dashboard's widgets.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../dashboards.dart';
import '../session.dart';
import 'failure_text.dart';
import 'list_scaffold.dart';
import 'oql_view.dart';

class DashboardsBody extends StatefulWidget {
  const DashboardsBody({
    super.key,
    required this.session,
    required this.dashboards,
  });

  final SessionController session;
  final DashboardsController dashboards;

  @override
  State<DashboardsBody> createState() => _DashboardsBodyState();
}

class _DashboardsBodyState extends State<DashboardsBody> {
  final _search = TextEditingController();

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback(
      (_) => widget.dashboards.refresh(),
    );
  }

  @override
  void dispose() {
    _search.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.dashboards;

    return ListenableBuilder(
      listenable: c,
      builder: (context, _) => ListScreen(
        controller: c,
        baseUrl: widget.session.baseUrl ?? '',
        search: SearchField(
          fieldKey: const Key('dashboards-search'),
          controller: _search,
          hint: l.dashboardsSearch,
          onSubmitted: (value) {
            c.query = value;
            c.refresh();
          },
        ),
        emptyTitle: l.dashboardsEmpty,
        itemBuilder: (context, i) =>
            _DashboardTile(summary: c.items[i], session: widget.session),
      ),
    );
  }
}

class _DashboardTile extends StatelessWidget {
  const _DashboardTile({required this.summary, required this.session});

  final DashboardSummary summary;
  final SessionController session;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    return Card(
      key: Key('dashboard-${summary.id}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: ListTile(
        title: Text(summary.name),
        subtitle: Text(
          l.dashboardWidgets(summary.widgetCount, summary.pageCount),
        ),
        trailing: const Icon(Icons.chevron_right),
        onTap: () {
          final client = session.client;
          if (client == null) return;
          Navigator.of(context).push(
            MaterialPageRoute<void>(
              builder: (_) => DashboardViewScreen(
                title: summary.name,
                controller: DashboardViewController(client, summary.id),
                baseUrl: session.baseUrl ?? '',
              ),
            ),
          );
        },
      ),
    );
  }
}

class DashboardViewScreen extends StatefulWidget {
  const DashboardViewScreen({
    super.key,
    required this.title,
    required this.controller,
    required this.baseUrl,
  });

  final String title;
  final DashboardViewController controller;
  final String baseUrl;

  @override
  State<DashboardViewScreen> createState() => _DashboardViewScreenState();
}

class _DashboardViewScreenState extends State<DashboardViewScreen> {
  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback(
      (_) => widget.controller.load(),
    );
  }

  @override
  void dispose() {
    widget.controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final c = widget.controller;

    return Scaffold(
      appBar: AppBar(title: Text(widget.title)),
      body: SafeArea(
        child: ListenableBuilder(
          listenable: c,
          builder: (context, _) {
            if (c.dashboard == null) {
              return c.failure != null
                  ? ListView(
                      padding: const EdgeInsets.all(16),
                      children: [
                        FailureBanner(
                          failure: c.failure,
                          baseUrl: widget.baseUrl,
                        ),
                      ],
                    )
                  : const Center(child: CircularProgressIndicator());
            }
            final widgets = c.widgets;
            return RefreshIndicator(
              onRefresh: c.load,
              child: ListView.builder(
                padding: const EdgeInsets.fromLTRB(12, 12, 12, 24),
                itemCount: widgets.length + 1,
                itemBuilder: (context, i) {
                  if (i == widgets.length) {
                    return c.loading
                        ? Padding(
                            padding: const EdgeInsets.all(16),
                            child: Text(
                              l.dashboardLoading,
                              textAlign: TextAlign.center,
                            ),
                          )
                        : const SizedBox.shrink();
                  }
                  final w = widgets[i];
                  return _WidgetCard(
                    widget: w,
                    result: c.results[w.id],
                    error: c.widgetErrors[w.id],
                  );
                },
              ),
            );
          },
        ),
      ),
    );
  }
}

/// One widget, rendered by what its query *answered* rather than by the
/// visualization the dashboard asked for.
///
/// The contract offers eight visualizations and four result kinds, and on a
/// 390-point screen the kinds are the useful distinction: a `facets` result is
/// a ranked list whether the dashboard called it a pie or a bar, and a ranked
/// list is both readable and precise where a five-slice pie is neither.
class _WidgetCard extends StatelessWidget {
  const _WidgetCard({
    required this.widget,
    required this.result,
    required this.error,
  });

  final DashboardWidget widget;
  final OqlResult? result;
  final String? error;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final text = Theme.of(context).textTheme;
    final scheme = Theme.of(context).colorScheme;

    return Card(
      key: Key('widget-${widget.id}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: Padding(
        padding: const EdgeInsets.all(14),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            if (widget.title.isNotEmpty) ...[
              Text(widget.title, style: text.titleSmall),
              const SizedBox(height: 10),
            ],
            _content(context, l, text, scheme),
          ],
        ),
      ),
    );
  }

  Widget _content(
    BuildContext context,
    L l,
    TextTheme text,
    ColorScheme scheme,
  ) {
    if (widget.markdown.trim().isNotEmpty) {
      // Shown as text, not rendered: a markdown renderer is a dependency for
      // the one widget type that is already readable without it.
      return Text(widget.markdown, style: text.bodyMedium);
    }
    if (error != null) {
      return Text(
        l.dashboardWidgetFailed,
        style: TextStyle(color: scheme.error),
      );
    }
    if (widget.query.trim().isEmpty) {
      return Text(l.dashboardNoQuery, style: text.bodySmall);
    }
    final r = result;
    if (r == null) {
      return const SizedBox(
        height: 24,
        child: Align(
          alignment: Alignment.centerLeft,
          child: SizedBox(
            width: 18,
            height: 18,
            child: CircularProgressIndicator(strokeWidth: 2),
          ),
        ),
      );
    }

    return OqlResultView(result: r, id: widget.id);
  }
}
