// One definition of what critical, warning and info look like.
//
// Built the way the web builds a badge: a tint of the semantic colour behind
// text in a separate, darker colour chosen for contrast (web/src/index.css
// keeps `--success` and `--success-text` apart for exactly that reason). Both
// screens got this wrong separately before it lived in one place -- taking
// `warning` from the theme's tertiary slot produced magenta.
import 'package:flutter/material.dart';

import 'theme.dart';

enum SeverityLevel { critical, warning, info, unknown }

/// Background and foreground for a severity chip.
({Color background, Color foreground}) severityChipColors(
  BuildContext context,
  SeverityLevel level,
) {
  final c = colorsOf(context);
  return switch (level) {
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
