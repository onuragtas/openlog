// One definition of what critical, warning and info look like.
//
// Built the way the web builds a badge: a tint of the semantic colour behind
// text in a separate, darker colour chosen for contrast (web/src/index.css
// keeps `--success` and `--success-text` apart for exactly that reason). Both
// screens got this wrong separately before it lived in one place -- taking
// `warning` from the theme's tertiary slot produced magenta.
import 'package:flutter/material.dart';

import '../../l10n/app_localizations.dart';
import '../api/schema.g.dart';
import 'theme.dart';

/// `good` is not a severity in the alerting sense; it is the other end of the
/// same scale, and a Core Web Vital rated good has to be green somewhere. It
/// lives here so there is still one answer to "what colour is this verdict".
enum SeverityLevel { good, critical, warning, info, unknown }

/// Background and foreground for a severity chip.
({Color background, Color foreground}) severityChipColors(
  BuildContext context,
  SeverityLevel level,
) {
  final c = colorsOf(context);
  return switch (level) {
    SeverityLevel.good => c.successBadge,
    SeverityLevel.critical => c.destructiveBadge,
    SeverityLevel.warning => c.warningBadge,
    SeverityLevel.info => (
      background: c.accent,
      foreground: c.accentForeground,
    ),
    SeverityLevel.unknown => c.neutralBadge,
  };
}

/// The colour for a severity shown as a word rather than a chip.
Color severityTextColor(BuildContext context, SeverityLevel level) {
  final c = colorsOf(context);
  return switch (level) {
    SeverityLevel.good => c.successText,
    SeverityLevel.critical => c.destructiveText,
    SeverityLevel.warning => c.warningText,
    SeverityLevel.info => c.mutedForeground,
    SeverityLevel.unknown => c.mutedForeground,
  };
}

/// OpenTelemetry severity numbers: 17+ is error, 13+ is warn, 1+ is info.
SeverityLevel severityOfNumber(int number) {
  if (number >= 17) return SeverityLevel.critical;
  if (number >= 13) return SeverityLevel.warning;
  if (number >= 1) return SeverityLevel.info;
  return SeverityLevel.unknown;
}

/// A severity as a filled chip. Shared because the alerts list and the incident
/// screen must not disagree about what "critical" looks like, which is the same
/// reason the colours above live here.
class SeverityChip extends StatelessWidget {
  const SeverityChip({super.key, required this.severity});

  final AlertSeverity severity;

  @override
  Widget build(BuildContext context) {
    final l = L.of(context);
    final (label, level) = switch (severity) {
      AlertSeverity.critical => (l.severityCritical, SeverityLevel.critical),
      AlertSeverity.warning => (l.severityWarning, SeverityLevel.warning),
      AlertSeverity.info => (l.severityInfo, SeverityLevel.info),
      // A severity this build has never heard of still has to render as
      // something, since the server can be newer than the app.
      AlertSeverity.unknown => (l.severityUnknown, SeverityLevel.unknown),
    };
    final colors = severityChipColors(context, level);
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
      decoration: BoxDecoration(
        color: colors.background,
        borderRadius: BorderRadius.circular(12),
      ),
      child: Text(
        label,
        style: TextStyle(
          color: colors.foreground,
          fontWeight: FontWeight.w600,
          fontSize: 12,
        ),
      ),
    );
  }
}

/// An attribute of an incident -- muted, flapping -- drawn as an outline rather
/// than a fill, so it cannot be mistaken for a severity.
class OutlineTag extends StatelessWidget {
  const OutlineTag({super.key, required this.label});

  final String label;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
      decoration: BoxDecoration(
        border: Border.all(color: scheme.outlineVariant),
        borderRadius: BorderRadius.circular(10),
      ),
      child: Text(
        label,
        style: TextStyle(color: scheme.onSurfaceVariant, fontSize: 11),
      ),
    );
  }
}

/// A tinted pill, built the way the web builds a badge.
class Tag extends StatelessWidget {
  const Tag({
    super.key,
    required this.label,
    this.level = SeverityLevel.unknown,
  });

  final String label;
  final SeverityLevel level;

  @override
  Widget build(BuildContext context) {
    final colors = severityChipColors(context, level);
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 3),
      decoration: BoxDecoration(
        color: colors.background,
        borderRadius: BorderRadius.circular(Radii.lg),
      ),
      child: Text(
        label,
        style: TextStyle(
          color: colors.foreground,
          fontSize: 11,
          fontWeight: FontWeight.w600,
        ),
      ),
    );
  }
}
