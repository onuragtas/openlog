// The web app's design tokens, as Dart.
//
// These are not "inspired by" the web: they are web/src/index.css converted
// from OKLCH to sRGB, token for token, so the two products are the same
// product. A seeded ColorScheme was what this app had before and it was wrong
// in a way nobody would have reported -- openlog is teal (#00736A) and the
// generated palette was blue.
//
// Regenerating after a change to index.css is arithmetic, not taste: convert
// oklch(L C H) with the standard OKLab matrices.
import 'package:flutter/material.dart';

/// One theme's worth of tokens, named as index.css names them.
class AppColors extends ThemeExtension<AppColors> {
  const AppColors({
    required this.background,
    required this.foreground,
    required this.card,
    required this.cardForeground,
    required this.muted,
    required this.mutedForeground,
    required this.border,
    required this.input,
    required this.ring,
    required this.primary,
    required this.primaryForeground,
    required this.secondary,
    required this.secondaryForeground,
    required this.accent,
    required this.accentForeground,
    required this.destructive,
    required this.destructiveForeground,
    required this.success,
    required this.warning,
    required this.successText,
    required this.warningText,
    required this.destructiveText,
    required this.sidebar,
    required this.chartGrid,
    required this.chartAxis,
  });

  final Color background;
  final Color foreground;
  final Color card;
  final Color cardForeground;
  final Color muted;
  final Color mutedForeground;
  final Color border;
  final Color input;
  final Color ring;
  final Color primary;
  final Color primaryForeground;
  final Color secondary;
  final Color secondaryForeground;
  final Color accent;
  final Color accentForeground;
  final Color destructive;
  final Color destructiveForeground;
  final Color success;
  final Color warning;

  /// Text on a tinted badge, which is a different colour from the fill: the
  /// web keeps these separate so a 15 % tint still passes WCAG AA.
  final Color successText;
  final Color warningText;
  final Color destructiveText;

  final Color sidebar;
  final Color chartGrid;
  final Color chartAxis;

  static const light = AppColors(
    background: Color(0xFFFCFCFC),
    foreground: Color(0xFF13161B),
    card: Color(0xFFFFFFFF),
    cardForeground: Color(0xFF13161B),
    muted: Color(0xFFF2F3F6),
    mutedForeground: Color(0xFF595E66),
    border: Color(0xFFDFE1E5),
    input: Color(0xFFD4D8DD),
    ring: Color(0xFF008479),
    primary: Color(0xFF00736A),
    primaryForeground: Color(0xFFFCFCFC),
    secondary: Color(0xFFEEF0F4),
    secondaryForeground: Color(0xFF1F2227),
    accent: Color(0xFFE8F1EF),
    accentForeground: Color(0xFF0F2623),
    destructive: Color(0xFFD02C2A),
    destructiveForeground: Color(0xFFFCFCFC),
    success: Color(0xFF298646),
    warning: Color(0xFFD0901E),
    successText: Color(0xFF09672E),
    warningText: Color(0xFF8D5406),
    destructiveText: Color(0xFFB7191C),
    sidebar: Color(0xFFF5F7F9),
    chartGrid: Color(0xFFE3E5E8),
    chartAxis: Color(0xFF5E646C),
  );

  static const dark = AppColors(
    background: Color(0xFF0D1013),
    foreground: Color(0xFFE9EBEF),
    card: Color(0xFF15171C),
    cardForeground: Color(0xFFE9EBEF),
    muted: Color(0xFF1F2227),
    mutedForeground: Color(0xFF9A9FA6),
    border: Color(0xFF2A2E34),
    input: Color(0xFF34383E),
    ring: Color(0xFF46B3A6),
    primary: Color(0xFF3DBBAE),
    primaryForeground: Color(0xFF071413),
    secondary: Color(0xFF24272B),
    secondaryForeground: Color(0xFFE9EBEF),
    accent: Color(0xFF1C3330),
    accentForeground: Color(0xFFDDF0ED),
    destructive: Color(0xFFF14E46),
    destructiveForeground: Color(0xFFF8F8F8),
    success: Color(0xFF54B66E),
    warning: Color(0xFFEDB345),
    successText: Color(0xFF83DC97),
    warningText: Color(0xFFF7C56D),
    destructiveText: Color(0xFFFF9F94),
    sidebar: Color(0xFF111418),
    chartGrid: Color(0xFF26292E),
    chartAxis: Color(0xFF9499A0),
  );

  /// A tinted badge, the way the web builds one: the fill at low opacity and a
  /// darker text colour chosen for contrast rather than derived from the fill.
  ({Color background, Color foreground}) get successBadge =>
      (background: success.withValues(alpha: 0.15), foreground: successText);

  ({Color background, Color foreground}) get warningBadge =>
      (background: warning.withValues(alpha: 0.20), foreground: warningText);

  ({Color background, Color foreground}) get destructiveBadge => (
    background: destructive.withValues(alpha: 0.15),
    foreground: destructiveText,
  );

  ({Color background, Color foreground}) get neutralBadge =>
      (background: secondary, foreground: secondaryForeground);

  @override
  AppColors copyWith() => this;

  @override
  AppColors lerp(AppColors? other, double t) =>
      t < 0.5 ? this : (other ?? this);
}

/// `--radius: 0.5rem`, and the four steps the web derives from it.
abstract final class Radii {
  static const sm = 4.0;
  static const md = 6.0;
  static const lg = 8.0;

  /// What a Card uses.
  static const xl = 12.0;
}

ThemeData openlogTheme(Brightness brightness) {
  final c = brightness == Brightness.dark ? AppColors.dark : AppColors.light;

  // Built from the tokens rather than from a seed. A seed would generate a
  // palette that is harmonious and not openlog's.
  final scheme = ColorScheme(
    brightness: brightness,
    primary: c.primary,
    onPrimary: c.primaryForeground,
    primaryContainer: c.accent,
    onPrimaryContainer: c.accentForeground,
    secondary: c.secondary,
    onSecondary: c.secondaryForeground,
    secondaryContainer: c.secondary,
    onSecondaryContainer: c.secondaryForeground,
    tertiary: c.primary,
    onTertiary: c.primaryForeground,
    error: c.destructive,
    onError: c.destructiveForeground,
    errorContainer: c.destructive.withValues(alpha: 0.15),
    onErrorContainer: c.destructiveText,
    surface: c.background,
    onSurface: c.foreground,
    surfaceContainerLowest: c.background,
    surfaceContainerLow: c.card,
    surfaceContainer: c.card,
    surfaceContainerHigh: c.muted,
    surfaceContainerHighest: c.muted,
    onSurfaceVariant: c.mutedForeground,
    outline: c.border,
    outlineVariant: c.border,
    inverseSurface: c.foreground,
    onInverseSurface: c.background,
    inversePrimary: c.primary,
    shadow: const Color(0x14000000),
    scrim: const Color(0x66000000),
  );

  final base = ThemeData(colorScheme: scheme, useMaterial3: true);

  return base.copyWith(
    extensions: [c],
    scaffoldBackgroundColor: c.background,
    dividerTheme: DividerThemeData(color: c.border, thickness: 1, space: 1),
    // A bordered card with almost no shadow, which is what `rounded-xl border
    // bg-card shadow-xs` is. Material's default is the opposite: elevation and
    // no border.
    cardTheme: CardThemeData(
      color: c.card,
      elevation: 0,
      margin: EdgeInsets.zero,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(Radii.xl),
        side: BorderSide(color: c.border),
      ),
    ),
    appBarTheme: AppBarTheme(
      backgroundColor: c.background,
      foregroundColor: c.foreground,
      surfaceTintColor: Colors.transparent,
      elevation: 0,
      scrolledUnderElevation: 0,
      shape: Border(bottom: BorderSide(color: c.border)),
    ),
    drawerTheme: DrawerThemeData(
      backgroundColor: c.sidebar,
      surfaceTintColor: Colors.transparent,
    ),
    inputDecorationTheme: InputDecorationTheme(
      filled: true,
      fillColor: c.card,
      isDense: true,
      border: OutlineInputBorder(
        borderRadius: BorderRadius.circular(Radii.md),
        borderSide: BorderSide(color: c.input),
      ),
      enabledBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(Radii.md),
        borderSide: BorderSide(color: c.input),
      ),
      focusedBorder: OutlineInputBorder(
        borderRadius: BorderRadius.circular(Radii.md),
        borderSide: BorderSide(color: c.ring, width: 2),
      ),
    ),
    filledButtonTheme: FilledButtonThemeData(
      // Shape and height only. Naming the colours here would paint
      // FilledButton.tonal in the primary too, and a tonal button is how a
      // per-row action stays quieter than the page's main one -- every alert
      // card carries an Acknowledge, and twenty solid teal buttons is a wall.
      style: FilledButton.styleFrom(
        // `h-9 … pointer-coarse:h-10`: the web already grows its buttons for
        // touch, and a phone is always touch.
        minimumSize: const Size(0, 40),
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(Radii.md),
        ),
      ),
    ),
    outlinedButtonTheme: OutlinedButtonThemeData(
      style: OutlinedButton.styleFrom(
        foregroundColor: c.foreground,
        side: BorderSide(color: c.input),
        minimumSize: const Size(0, 40),
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(Radii.md),
        ),
      ),
    ),
    textButtonTheme: TextButtonThemeData(
      style: TextButton.styleFrom(foregroundColor: c.primary),
    ),
    progressIndicatorTheme: ProgressIndicatorThemeData(
      color: c.primary,
      linearTrackColor: c.muted,
    ),
    listTileTheme: ListTileThemeData(
      iconColor: c.mutedForeground,
      textColor: c.foreground,
    ),
  );
}

/// The tokens of the theme in scope.
AppColors colorsOf(BuildContext context) =>
    Theme.of(context).extension<AppColors>() ?? AppColors.light;

/// The families to try for text that has to line up: a log body, a JSON
/// document, an attribute value.
///
/// Not `fontFamily: 'monospace'`: there is no font by that name on iOS, so
/// asking for one silently gets the default font on half the devices (the
/// same trap the holiday calendar list fell into). `Menlo` is Apple's,
/// `Roboto Mono` and `monospace` are Android's, and the list is tried in
/// order.
const monoFamilies = ['Menlo', 'Roboto Mono', 'monospace', 'Courier New'];

/// A monospaced version of [style].
TextStyle? mono(TextStyle? style) =>
    style?.copyWith(fontFamilyFallback: monoFamilies);
