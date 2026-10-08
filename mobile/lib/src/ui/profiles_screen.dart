// Profiling: what has been profiled, and where the time (or the memory) went.
//
// No flame graph. It wants width this screen does not have, and the ranked
// function list is the part of the answer a phone can show honestly.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import '../detail.dart';
import '../sections.dart';
import '../session.dart';
import 'detail_scaffold.dart';
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

class _ProfileScreenState extends State<ProfileScreen> {
  late final ProfileFunctionsController _c;

  @override
  void initState() {
    super.initState();
    _c = widget.sections.profileFunctions(
      service: widget.profile.service,
      type: widget.profile.type,
      environment: widget.profile.environment,
    );
    WidgetsBinding.instance.addPostFrameCallback((_) => _c.refresh());
  }

  @override
  void dispose() {
    _c.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);

    return ListenableBuilder(
      listenable: _c,
      builder: (context, _) => DetailScreen<ProfileFunctionPage>(
        controller: _c,
        baseUrl: widget.session.baseUrl ?? '',
        title: widget.profile.service,
        subtitle: widget.profile.type,
        builder: (context, page) => _body(context, l, page),
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
      return '${(value / 1000).toStringAsFixed(0)} µs';
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
