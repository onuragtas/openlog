// The dates of a holiday calendar, as typed.
//
// Two shapes, both accepted by the contract: `YYYY-MM-DD` is one day,
// `MM-DD` (and `--MM-DD`, the iCalendar spelling) is that day every year. The
// web parses the same two in `web/src/lib/mute-schedule.ts`; a phone that
// accepted fewer would make a calendar that cannot be edited where it was
// made.
library;

/// What [parseCalendarDates] made of the text: the dates it understood, and
/// the entries it did not.
///
/// The invalid ones are kept rather than dropped so the form can name them.
/// Silently discarding "31-02" would save a calendar missing a day nobody
/// notices is missing until an alert fires on a holiday.
class CalendarDates {
  const CalendarDates(this.dates, this.invalid);

  /// Sorted and deduplicated, which is also how the server stores them.
  final List<String> dates;
  final List<String> invalid;
}

/// Parses one date per line, or comma-separated.
///
/// Yearly dates sort before dated ones here (`04-23` before `2026-03-20`)
/// because that is plain string order and the server returns them the same
/// way; the screen does not reorder what it is shown.
CalendarDates parseCalendarDates(String text) {
  final dates = <String>{};
  final invalid = <String>[];
  for (final raw in text.split(RegExp(r'[\n,]+'))) {
    final trimmed = raw.trim();
    if (trimmed.isEmpty) continue;
    // `--MM-DD` and `MM-DD` are the same date; the server stores the short one.
    final v = trimmed.startsWith('--') ? trimmed.substring(2) : trimmed;
    final yearly = RegExp(r'^(\d{2})-(\d{2})$').firstMatch(v);
    if (yearly != null) {
      final month = int.parse(yearly[1]!);
      final day = int.parse(yearly[2]!);
      // 2000 is a leap year, so 02-29 is a date somebody can mark every year.
      if (_real(2000, month, day)) {
        dates.add(v);
      } else {
        invalid.add(trimmed);
      }
      continue;
    }
    final one = RegExp(r'^(\d{4})-?(\d{2})-?(\d{2})$').firstMatch(v);
    final year = one == null ? 0 : int.parse(one[1]!);
    final month = one == null ? 0 : int.parse(one[2]!);
    final day = one == null ? 0 : int.parse(one[3]!);
    if (one != null && _real(year, month, day)) {
      dates.add(
        '${one[1]}-${month.toString().padLeft(2, '0')}'
        '-${day.toString().padLeft(2, '0')}',
      );
    } else {
      invalid.add(trimmed);
    }
  }
  final sorted = dates.toList()..sort();
  return CalendarDates(sorted, invalid);
}

/// Whether the day exists. `DateTime(2026, 2, 31)` is March 3rd rather than an
/// error, so the only way to reject it is to build it and look.
bool _real(int year, int month, int day) {
  if (month < 1 || month > 12 || day < 1 || day > 31) return false;
  final d = DateTime.utc(year, month, day);
  return d.month == month && d.day == day;
}
