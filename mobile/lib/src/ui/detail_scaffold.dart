// The shape every detail screen shares: a title, a spinner on the first load, a
// failure banner, pull-to-refresh.
import 'package:flutter/material.dart';

import '../detail.dart';
import 'failure_text.dart';

class DetailScreen<T> extends StatelessWidget {
  const DetailScreen({
    super.key,
    required this.controller,
    required this.baseUrl,
    required this.title,
    required this.builder,
    this.subtitle,
  });

  final DetailController<T> controller;
  final String baseUrl;
  final String title;
  final String? subtitle;

  /// The screen's content, called only once there is something to show.
  final List<Widget> Function(BuildContext, T) builder;

  @override
  Widget build(BuildContext context) {
    final value = controller.value;
    final banner = FailureBanner(failure: controller.failure, baseUrl: baseUrl);

    return Scaffold(
      appBar: AppBar(
        title: subtitle == null
            ? Text(title, overflow: TextOverflow.ellipsis)
            : Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                mainAxisAlignment: MainAxisAlignment.center,
                children: [
                  Text(title, overflow: TextOverflow.ellipsis),
                  Text(
                    subtitle!,
                    overflow: TextOverflow.ellipsis,
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                ],
              ),
      ),
      body: RefreshIndicator(
        onRefresh: controller.refresh,
        child: controller.loadingFirst
            ? const Center(child: CircularProgressIndicator())
            : ListView(
                padding: const EdgeInsets.fromLTRB(16, 12, 16, 32),
                children: [
                  // The banner sits above the content, not below it: on a
                  // detail screen the failure is usually about the whole screen
                  // rather than about one row of it.
                  banner,
                  if (value != null) ...builder(context, value),
                ],
              ),
      ),
    );
  }
}

/// A labelled block of a detail screen.
class DetailSection extends StatelessWidget {
  const DetailSection({super.key, required this.title, required this.children});

  final String title;
  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Padding(
      padding: const EdgeInsets.only(top: 22),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            title,
            style: text.titleSmall?.copyWith(
              color: Theme.of(context).colorScheme.onSurfaceVariant,
            ),
          ),
          const SizedBox(height: 8),
          ...children,
        ],
      ),
    );
  }
}

/// One number with its name under it, the way the web stacks a stat.
class Stat extends StatelessWidget {
  const Stat({super.key, required this.label, required this.value, this.color});

  final String label;
  final String value;
  final Color? color;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(value, style: theme.textTheme.titleLarge?.copyWith(color: color)),
        Text(
          label,
          style: theme.textTheme.bodySmall?.copyWith(
            color: theme.colorScheme.onSurfaceVariant,
          ),
        ),
      ],
    );
  }
}

/// A key and its value on one line, wrapping rather than overflowing: a label
/// value can be a Kubernetes pod name, and Turkish labels are longer than
/// English ones.
class KeyValue extends StatelessWidget {
  const KeyValue({super.key, required this.name, required this.value});

  final String name;
  final String value;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.only(bottom: 6),
      child: Wrap(
        spacing: 8,
        children: [
          Text(
            name,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
          ),
          Text(value, style: theme.textTheme.bodyMedium),
        ],
      ),
    );
  }
}
