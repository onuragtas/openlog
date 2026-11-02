// The numbers of the APM and database screens, as the web writes them
// (`web/src/lib/apm.ts`), so the same value reads the same on both.
//
// A missing value is a dash and never a zero: no measurement and a measured
// nothing are different answers.

/// [value] with at most [digits] decimals and no trailing zeros, which is what
/// the web's `maximumFractionDigits` does.
String _trimmed(double value, int digits) {
  final text = value.toStringAsFixed(digits);
  if (!text.contains('.')) return text;
  return text.replaceFirst(RegExp(r'\.?0+$'), '');
}

/// A duration, in the unit it reads best in.
String formatApmMs(double? ms) {
  if (ms == null || !ms.isFinite) return '–';
  if (ms < 1) return '${_trimmed(ms, 2)} ms';
  if (ms < 1000) return '${_trimmed(ms, ms < 10 ? 1 : 0)} ms';
  if (ms < 60000) return '${_trimmed(ms / 1000, 2)} s';
  return '${_trimmed(ms / 60000, 1)} min';
}

/// Requests per minute.
String formatApmRpm(double? v) {
  if (v == null || !v.isFinite) return '–';
  return '${_trimmed(v, v < 10 ? 2 : 1)} rpm';
}

/// A 0..1 rate as a percentage, with more precision below one percent.
String formatApmRate(double? v) {
  if (v == null || !v.isFinite) return '–';
  final percent = v * 100;
  if (percent == 0) return '0%';
  if (percent < 1) return '${percent.toStringAsFixed(2)}%';
  return '${_trimmed(percent, 1)}%';
}

String formatApmApdex(double? v) =>
    v == null || !v.isFinite ? '–' : v.toStringAsFixed(2);
