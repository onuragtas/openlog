// What a mute is doing now, and the form that writes one.
//
// A port of `web/src/lib/mute-schedule.ts` and of the form in
// `web/src/components/alerts/MutesManager.tsx`: weekly days or a monthly rule
// and the RFC 5545 subset the API accepts. Both sides have to agree on which
// rules a form can represent, because one it cannot is kept as it came rather
// than quietly rewritten.
import 'api/schema.g.dart';

/// In the order the web sorts them: what is silencing alerts comes first.
enum MuteStatus { active, scheduled, ended }

/// The server's `active` wins; the web's rule.
MuteStatus muteStatus(AlertMute m, DateTime now) {
  if (m.active) return MuteStatus.active;
  if (m.schedule != null) {
    return now.isBefore(m.endsAt) ? MuteStatus.scheduled : MuteStatus.ended;
  }
  if (now.isBefore(m.startsAt)) return MuteStatus.scheduled;
  return now.isBefore(m.endsAt) ? MuteStatus.active : MuteStatus.ended;
}

const weekDays = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];

const _codes = {
  'mon': 'MO',
  'tue': 'TU',
  'wed': 'WE',
  'thu': 'TH',
  'fri': 'FR',
  'sat': 'SA',
  'sun': 'SU',
};

/// 1..4 are first to fourth, and -1 is the last.
const muteOrdinals = [1, 2, 3, 4, -1];

/// The weekday choice of a monthly rule that means any of Monday to Friday.
const anyWeekday = 'weekday';

/// A monthly rule as the form holds it.
class MonthlyForm {
  const MonthlyForm({
    this.mode = 'monthday',
    this.monthDays = '1',
    this.ordinal = 1,
    this.day = 'mon',
  });

  /// `monthday` or `weekday`.
  final String mode;

  /// "1, 15, -1": 1..31, and -1 is the last day.
  final String monthDays;
  final int ordinal;

  /// One of [weekDays] or [anyWeekday].
  final String day;

  MonthlyForm copyWith({
    String? mode,
    String? monthDays,
    int? ordinal,
    String? day,
  }) => MonthlyForm(
    mode: mode ?? this.mode,
    monthDays: monthDays ?? this.monthDays,
    ordinal: ordinal ?? this.ordinal,
    day: day ?? this.day,
  );
}

/// "1, 15, -1" as sorted unique days, or null when one is invalid or there is
/// none at all.
List<int>? parseMonthDays(String text) {
  final parts = text.split(RegExp(r'[,\s]+')).where((p) => p.isNotEmpty);
  if (parts.isEmpty) return null;
  final out = <int>{};
  for (final p in parts) {
    if (!RegExp(r'^[-+]?\d+$').hasMatch(p)) return null;
    final n = int.parse(p);
    if (n == 0 || n < -31 || n > 31) return null;
    out.add(n);
  }
  return out.toList()..sort();
}

/// The rule of a monthly form, or null when its day list is invalid.
String? monthlyRRule(MonthlyForm f) {
  if (f.mode == 'monthday') {
    final days = parseMonthDays(f.monthDays);
    return days == null ? null : 'FREQ=MONTHLY;BYMONTHDAY=${days.join(',')}';
  }
  if (f.day == anyWeekday) {
    return 'FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=${f.ordinal}';
  }
  return 'FREQ=MONTHLY;BYDAY=${f.ordinal}${_codes[f.day]}';
}

/// The form of a stored rule, or null for one it cannot represent.
MonthlyForm? parseMonthlyRRule(String? rrule) {
  if (rrule == null || rrule.isEmpty) return null;
  final parts = <String, String>{};
  for (final p in rrule.toUpperCase().replaceFirst('RRULE:', '').split(';')) {
    if (p.isEmpty) continue;
    final i = p.indexOf('=');
    parts[i < 0 ? p : p.substring(0, i)] = i < 0 ? '' : p.substring(i + 1);
  }
  if (parts['FREQ'] != 'MONTHLY') return null;
  final keys = parts.keys.where((k) => k != 'FREQ' && k != 'INTERVAL').toList();
  final monthDay = parts['BYMONTHDAY'];
  if (keys.length == 1 && monthDay != null) {
    final days = parseMonthDays(monthDay.replaceAll(',', ', '));
    return days == null ? null : MonthlyForm(monthDays: days.join(', '));
  }
  int? ordinal(String? s) {
    final n = int.tryParse(s ?? '');
    return n != null && muteOrdinals.contains(n) ? n : null;
  }

  final byDay = parts['BYDAY'];
  if (keys.length == 2 && byDay == 'MO,TU,WE,TH,FR') {
    final o = ordinal(parts['BYSETPOS']);
    return o == null
        ? null
        : MonthlyForm(mode: 'weekday', ordinal: o, day: anyWeekday);
  }
  if (keys.length == 1 && byDay != null) {
    final m = RegExp(r'^([-+]?\d)([A-Z]{2})$').firstMatch(byDay);
    if (m == null) return null;
    final o = ordinal(m.group(1));
    String? day;
    for (final e in _codes.entries) {
      if (e.value == m.group(2)) day = e.key;
    }
    return o == null || day == null
        ? null
        : MonthlyForm(mode: 'weekday', ordinal: o, day: day);
  }
  return null;
}

/// `2026-10-29` from a local day: the form's exception dates.
String dateOnly(DateTime d) =>
    '${d.year.toString().padLeft(4, '0')}-'
    '${d.month.toString().padLeft(2, '0')}-'
    '${d.day.toString().padLeft(2, '0')}';

/// Whether a schedule's own time reads `22:00`.
bool _isTime(String t) => RegExp(r'^\d{2}:\d{2}$').hasMatch(t);

/// Everything the web's form holds, and what it sends.
class MuteDraft {
  MuteDraft.from(AlertMute? mute, {DateTime? now})
    : name = mute?.name ?? '',
      comment = mute?.comment ?? '',
      starts = mute?.startsAt.toLocal() ?? (now ?? DateTime.now()),
      ends =
          mute?.endsAt.toLocal() ??
          (now ?? DateTime.now()).add(const Duration(hours: 2)),
      ruleIds = [...?mute?.ruleIds],
      matchers = [
        for (final m in mute?.matchers ?? const <AlertMuteMatcher>[])
          MuteMatcherDraft(m.label, m.op.wire, m.value),
      ],
      recurring = mute?.schedule != null,
      recurrence = mute?.schedule?.rrule?.startsWith('FREQ=MONTHLY') ?? false
          ? 'monthly'
          : 'weekly',
      monthly = parseMonthlyRRule(mute?.schedule?.rrule) ?? const MonthlyForm(),
      customRRule =
          (mute?.schedule?.rrule?.startsWith('FREQ=MONTHLY') ?? false) &&
              parseMonthlyRRule(mute?.schedule?.rrule) == null
          ? mute?.schedule?.rrule
          : null,
      timezone = mute?.schedule?.timezone ?? 'UTC',
      days = (mute?.schedule?.days.isNotEmpty ?? false)
          ? [for (final d in mute!.schedule!.days) d.wire]
          : ['mon', 'tue', 'wed', 'thu', 'fri'],
      startTime = mute?.schedule?.startTime ?? '22:00',
      endTime = mute?.schedule?.endTime ?? '06:00',
      from = mute?.schedule?.from,
      until = mute?.schedule?.until,
      exdates = [...?mute?.schedule?.exdates],
      calendarIds = [...?mute?.schedule?.holidayCalendarIds];

  String name;
  String comment;
  DateTime starts;
  DateTime ends;
  List<String> ruleIds;
  List<MuteMatcherDraft> matchers;
  bool recurring;

  /// `weekly` or `monthly`.
  String recurrence;
  MonthlyForm monthly;

  /// A stored monthly rule the form cannot represent, kept as it came until
  /// somebody picks another shape.
  String? customRRule;
  String timezone;
  List<String> days;
  String startTime;
  String endTime;
  final DateTime? from;
  final DateTime? until;
  List<String> exdates;
  List<String> calendarIds;

  String? get rrule =>
      recurrence == 'monthly' ? customRRule ?? monthlyRRule(monthly) : null;

  /// A monthly day list that does not parse, which holds the save button back.
  bool get monthDaysInvalid =>
      recurring &&
      recurrence == 'monthly' &&
      monthly.mode == 'monthday' &&
      customRRule == null &&
      rrule == null;

  /// Whether the schedule is whole enough to ask when it would next run.
  bool get previewable =>
      recurring &&
      (recurrence == 'weekly' ? days.isNotEmpty : rrule != null) &&
      _isTime(startTime) &&
      _isTime(endTime);

  Map<String, Object?> get schedule => {
    'timezone': timezone.trim(),
    if (recurrence == 'weekly')
      'days': [
        for (final d in weekDays)
          if (days.contains(d)) d,
      ]
    else
      'rrule': rrule ?? '',
    'start_time': startTime,
    'end_time': endTime,
    'from': from?.toUtc().toIso8601String(),
    'until': until?.toUtc().toIso8601String(),
    'exdates': exdates,
    'holiday_calendar_ids': calendarIds,
  };

  /// An `AlertMuteInput` of the contract, the same one the web sends.
  Map<String, Object?> toInput() => {
    'name': name.trim(),
    'comment': comment,
    'rule_ids': ruleIds,
    'matchers': [
      for (final m in matchers)
        if (m.label.trim().isNotEmpty) m.toJson(),
    ],
    if (recurring)
      'schedule': schedule
    else ...{
      'starts_at': starts.toUtc().toIso8601String(),
      'ends_at': ends.toUtc().toIso8601String(),
      'schedule': null,
    },
  };
}

class MuteMatcherDraft {
  MuteMatcherDraft([this.label = '', this.op = 'eq', this.value = '']);

  String label;

  /// `eq`, `neq` or `contains`.
  String op;
  String value;

  Map<String, Object?> toJson() => {'label': label, 'op': op, 'value': value};
}
