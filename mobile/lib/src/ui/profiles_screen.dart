// Profiling: what has been profiled, and where the time (or the memory) went.
//
// One profile has the web's two tabs: the flame graph and the ranked
// functions. The flame graph was left out once, on the grounds that it wants
// width a phone does not have -- but the web's own answer to a frame too
// narrow to read is to zoom into it, and that works on a phone too.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
import 'flame_view.dart';
import 'list_scaffold.dart';
import 'sections_screen.dart';
import 'theme.dart';

class ProfilesBody extends StatelessWidget {
  const ProfilesBody({
    super.key,
    required this.session,
    required this.sections,
    required this.active,
  });

  final SessionController session;
  final Sections sections;
  final bool active;

  @override
  Widget build(BuildContext context) => SectionBody<ProfileService>(
    session: session,
    controller: sections.profiles,
    searchKey: 'profiles-search',
    searchHint: (l) => l.profilesSearch,
    active: active,
    emptyTitle: (l) => l.profilesEmpty,
    card: (context, p) => _ProfileCard(
      profile: p,
      onOpen: () => Navigator.of(context).push(
        MaterialPageRoute<void>(
          builder: (_) =>
              ProfileScreen(session: session, sections: sections, profile: p),
        ),
      ),
    ),
  );
}

class _ProfileCard extends StatelessWidget {
  const _ProfileCard({required this.profile, required this.onOpen});

  final ProfileService profile;
  final VoidCallback onOpen;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    return Card(
      key: Key('profile-${profile.service}-${profile.type}'),
      margin: const EdgeInsets.symmetric(vertical: 6, horizontal: 4),
      child: InkWell(
        onTap: onOpen,
        borderRadius: BorderRadius.circular(Radii.lg),
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Expanded(
                    child: Text(
                      profile.service,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.titleSmall,
                    ),
                  ),
                  const SizedBox(width: 8),
                  // The type is the thing that makes the numbers mean
                  // something: nanoseconds and bytes do not add up.
                  Text(profile.type, style: theme.textTheme.bodyMedium),
                ],
              ),
              const SizedBox(height: 6),
              Wrap(
                spacing: 8,
                runSpacing: 4,
                children: [
                  if (profile.environment.isNotEmpty)
                    Text(profile.environment, style: muted),
                  Text(l.profileSamples(profile.samples), style: muted),
                  Text(
                    formatProfileValue(profile.total, profile.unit),
                    style: muted,
                  ),
                  Text(relativeTimeOf(l, profile.lastSeen), style: muted),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class ProfileScreen extends StatefulWidget {
  const ProfileScreen({
    super.key,
    required this.session,
    required this.sections,
    required this.profile,
  });

  final SessionController session;
  final Sections sections;
  final ProfileService profile;

  @override
  State<ProfileScreen> createState() => _ProfileScreenState();
}

class _ProfileScreenState extends State<ProfileScreen>
    with SingleTickerProviderStateMixin {
  late final ProfileFunctionsController _c;
  late final ProfileFlameController _flame;
  late final TabController _tabs;

  @override
  void initState() {
    super.initState();
    _c = widget.sections.profileFunctions(
      service: widget.profile.service,
      type: widget.profile.type,
      environment: widget.profile.environment,
    );
    _flame = widget.sections.profileFlame(
      service: widget.profile.service,
      type: widget.profile.type,
      environment: widget.profile.environment,
    );
    _tabs = TabController(length: 2, vsync: this)
      ..addListener(() {
        if (_tabs.indexIsChanging) return;
        // The functions are a second request about the same window, so
        // they are asked for when that tab is looked at rather than
        // alongside the graph.
        if (_tabs.index == 1 && !_c.loaded && !_c.loadingFirst) _c.refresh();
      });
    // The flame graph is the tab that opens, as it is on the web.
    WidgetsBinding.instance.addPostFrameCallback((_) => _flame.refresh());
  }

  @override
  void dispose() {
    _tabs.dispose();
    _flame.dispose();
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final theme = Theme.of(context);

    return Scaffold(
      appBar: AppBar(
        title: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            Text(widget.profile.service, overflow: TextOverflow.ellipsis),
            Text(
              widget.profile.type,
              overflow: TextOverflow.ellipsis,
              style: theme.textTheme.bodySmall,
            ),
          ],
        ),
        bottom: TabBar(
          controller: _tabs,
          tabs: [
            Tab(key: const Key('profile-tab-flame'), text: l.profileTabFlame),
            Tab(
              key: const Key('profile-tab-functions'),
              text: l.profileTabFunctions,
            ),
          ],
        ),
      ),
      body: TabBarView(
        controller: _tabs,
        children: [
          ListenableBuilder(
            listenable: _flame,
            builder: (context, _) => DetailBody<ProfileFlame>(
              controller: _flame,
              baseUrl: widget.session.baseUrl ?? '',
              builder: (context, flame) => [
                FlameView(
                  key: const Key('flame'),
                  flame: flame.flame,
                  unit: flame.unit,
                ),
              ],
            ),
          ),
          ListenableBuilder(
            listenable: _c,
            builder: (context, _) => DetailBody<ProfileFunctionPage>(
              controller: _c,
              baseUrl: widget.session.baseUrl ?? '',
              builder: (context, page) => _body(context, l, page),
            ),
          ),
        ],
      ),
    );
  }

  List<Widget> _body(BuildContext context, L l, ProfileFunctionPage page) {
    final theme = Theme.of(context);
    final colors = colorsOf(context);
    final muted = theme.textTheme.bodySmall?.copyWith(
      color: theme.colorScheme.onSurfaceVariant,
    );

    if (page.functions.isEmpty) {
      return [
        const SizedBox(height: 48),
        Text(
          l.profileNoFunctions,
          textAlign: TextAlign.center,
          style: theme.textTheme.titleMedium,
        ),
      ];
    }

    return [
      DetailSection(
        title: l.profileFunctions,
        children: [
          for (final f in page.functions)
            Padding(
              key: Key('fn-${f.function}'),
              padding: const EdgeInsets.only(bottom: 10),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(
                    children: [
                      Expanded(
                        child: Text(
                          f.function,
                          maxLines: 2,
                          overflow: TextOverflow.ellipsis,
                          style: theme.textTheme.bodyMedium,
                        ),
                      ),
                      const SizedBox(width: 8),
                      Text(
                        formatProfileValue(f.self, page.unit),
                        style: theme.textTheme.bodyMedium?.copyWith(
                          fontWeight: FontWeight.w600,
                        ),
                      ),
                    ],
                  ),
                  const SizedBox(height: 3),
                  Row(
                    children: [
                      Expanded(
                        child: LinearProgressIndicator(
                          value: _c.shareOf(f).clamp(0.0, 1.0),
                          backgroundColor: colors.muted,
                          color: colors.primary,
                        ),
                      ),
                      const SizedBox(width: 10),
                      Text(
                        l.profileShare(
                          (_c.shareOf(f) * 100).toStringAsFixed(1),
                        ),
                        style: muted,
                      ),
                    ],
                  ),
                ],
              ),
            ),
          const SizedBox(height: 4),
          Text(l.profileOfShown, style: muted),
        ],
      ),
    ];
  }
}

/// A profile value in the unit the profile itself declared.
///
/// The unit comes from the data, never from the type's name: a profile can
/// measure nanoseconds, bytes or plain counts, and reading 4000000000 as four
/// seconds when it is four gigabytes would be the worst kind of wrong.
String formatProfileValue(int value, String unit) {
  switch (unit) {
    case 'nanoseconds':
      if (value >= 1000000000) {
        return '${(value / 1000000000).toStringAsFixed(2)} s';
      }
      if (value >= 1000000) {
        return '${(value / 1000000).toStringAsFixed(0)} ms';
      }
      // Under a microsecond it is nanoseconds, as the web prints them:
      // rounding 100 ns to "0 µs" reads as nothing at all, and in a flame
      // graph the small frames are half the picture.
      if (value >= 1000) {
        return '${(value / 1000).toStringAsFixed(0)} µs';
      }
      return '$value ns';
    case 'bytes':
      if (value >= 1 << 30) {
        return '${(value / (1 << 30)).toStringAsFixed(2)} GiB';
      }
      if (value >= 1 << 20) {
        return '${(value / (1 << 20)).toStringAsFixed(1)} MiB';
      }
      if (value >= 1 << 10) {
        return '${(value / (1 << 10)).toStringAsFixed(0)} KiB';
      }
      return '$value B';
    default:
      // An honest fallback: the number, and the unit as the profile named it.
      return unit.isEmpty ? '$value' : '$value $unit';
  }
}
