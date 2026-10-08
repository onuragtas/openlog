// One definition of what critical, warning and info look like.
//
// Severity is semantic the way an error colour is: it has to mean the same
// thing at a glance, in light and dark, on an alert chip and on a log line.
// Taking it from the theme's container slots does not promise that -- against
// this app's blue seed, `tertiary` is magenta, which reads as a second kind of
// critical. Both screens got that wrong separately before this existed.
import 'package:flutter/material.dart';

/// Background and foreground for a severity chip.
({Color background, Color foreground}) severityChipColors(
  BuildContext context,
  SeverityLevel level,
) {
  final dark = Theme.of(context).brightness == Brightness.dark;
  return switch (level) {
    SeverityLevel.critical => (
      background: dark ? const Color(0xFF5C1A1A) : const Color(0xFFFFDAD6),
      foreground: dark ? const Color(0xFFFFB4AB) : const Color(0xFF8C1D18),
    ),
    SeverityLevel.warning => (
      background: dark ? const Color(0xFF4A3400) : const Color(0xFFFFE28A),
      foreground: dark ? const Color(0xFFFFD166) : const Color(0xFF6B4E00),
    ),
    SeverityLevel.info => (
      background: dark ? const Color(0xFF1E3A5F) : const Color(0xFFD8E6FF),
      foreground: dark ? const Color(0xFFADC9F5) : const Color(0xFF1B3A66),
    ),
    SeverityLevel.unknown => (
      background: Theme.of(context).colorScheme.surfaceContainerHighest,
      foreground: Theme.of(context).colorScheme.onSurface,
    ),
  };
}

/// The text colour for a severity shown as a word rather than a chip.
Color severityTextColor(BuildContext context, SeverityLevel level) {
  if (level == SeverityLevel.unknown) {
    return Theme.of(context).colorScheme.onSurfaceVariant;
  }
  return severityChipColors(context, level).foreground;
}

enum SeverityLevel { critical, warning, info, unknown }

/// OpenTelemetry severity numbers: 17+ is error, 13+ is warn, 9+ is info.
SeverityLevel severityOfNumber(int number) {
  if (number >= 17) return SeverityLevel.critical;
  if (number >= 13) return SeverityLevel.warning;
  if (number >= 1) return SeverityLevel.info;
  return SeverityLevel.unknown;
}
