// Making an alert rule from a recommended template.
//
// The same two steps as the web (`web/src/lib/alert-templates.ts`): the form
// values are converted to the API's parameters -- a ratio is edited as a
// percentage because "80" is what people mean by 80% -- and then the server
// renders, previews and stores. Nothing about the rule itself is computed
// here: the threshold, the window and the condition are the server's.
import 'package:flutter/foundation.dart';

import 'api/client.dart';
import 'api/schema.g.dart';
import 'list_controller.dart';
import 'session.dart';

/// The categories, in the catalog's own order.
const templateCategories = <String>[
  'host',
  'container',
  'apm',
  'integration',
  'kubernetes',
];

/// English or Turkish, which is all the contract offers.
String templateLanguage(String locale) => locale.startsWith('tr') ? 'tr' : 'en';

/// The text for [locale], falling back to English rather than to nothing: a
/// template with no Turkish name is still a template somebody can use.
String templateText(AlertTemplateText? text, String locale) {
  if (text == null) return '';
  final tr = templateLanguage(locale) == 'tr';
  final picked = tr ? text.tr : text.en;
  return picked.isEmpty ? text.en : picked;
}

/// Parameters the person fills in.
///
/// `host_name` and `discovery_id` are the catalog's way of carrying a target
/// the page already knows; on a phone the list is not opened from a host, so
/// they are left to the server and `instance` with them.
List<AlertTemplateParam> editableParams(AlertTemplate template) => [
  for (final p in template.params)
    if (p.key != 'host_name' && p.key != 'discovery_id' && p.key != 'instance')
      p,
];

/// Whether the template can be set up without a target to hang it on.
///
/// Ratio templates compute their threshold from a reference metric on one
/// instance, so without an instance there is nothing to take a ratio of.
bool templateUsable(AlertTemplate t) =>
    t.referenceMetric == null || t.referenceMetric!.isEmpty;

double _round(double v) => (v * 1e6).roundToDouble() / 1e6;

/// The form value of a parameter's default. A ratio becomes a percentage.
String initialValue(AlertTemplateParam p) {
  final d = p.defaultValue;
  if (d == null || d == '') return '';
  if (d is num) {
    final v = d.toDouble();
    final shown = p.unit == 'ratio' ? _round(v * 100) : v;
    // Whole numbers without a trailing `.0`: a threshold of 80 is typed as
    // "80", and "80.0" in the box invites a decimal nobody meant.
    return shown == shown.roundToDouble()
        ? shown.toInt().toString()
        : shown.toString();
  }
  return '$d';
}

Map<String, String> initialValues(List<AlertTemplateParam> params) => {
  for (final p in params) p.key: initialValue(p),
};

/// Why a value cannot be sent: 'required', 'number' or 'range'.
class ParamError {
  const ParamError(this.kind, {this.min, this.max});

  final String kind;
  final double? min;
  final double? max;
}

/// The range as it is typed, which for a ratio is in percent.
({double? min, double? max}) displayRange(AlertTemplateParam p) {
  final f = p.unit == 'ratio' ? 100.0 : 1.0;
  return (
    min: p.min == null ? null : _round(p.min! * f),
    max: p.max == null ? null : _round(p.max! * f),
  );
}

bool _numeric(AlertTemplateParam p) =>
    p.kind == AlertTemplateParamKind.number ||
    p.kind == AlertTemplateParamKind.duration;

/// Form values to API parameters, with the errors the server would give
/// anyway -- found here so the phone does not need a round trip to say that
/// 150% is not a ratio.
({Map<String, Object?> params, Map<String, ParamError> errors}) buildParams(
  List<AlertTemplateParam> params,
  Map<String, String> values,
) {
  final out = <String, Object?>{};
  final errors = <String, ParamError>{};
  for (final p in params) {
    final raw = (values[p.key] ?? '').trim();
    if (raw.isEmpty) {
      if (p.required) errors[p.key] = const ParamError('required');
      continue;
    }
    if (!_numeric(p)) {
      out[p.key] = raw;
      continue;
    }
    // A comma is a decimal point on a Turkish keyboard, and the number pad
    // offers whichever the locale picked.
    final n = double.tryParse(raw.replaceAll(',', '.'));
    if (n == null || !n.isFinite) {
      errors[p.key] = const ParamError('number');
      continue;
    }
    final r = displayRange(p);
    if ((r.min != null && n < r.min!) || (r.max != null && n > r.max!)) {
      errors[p.key] = ParamError('range', min: r.min, max: r.max);
      continue;
    }
    out[p.key] = p.unit == 'ratio' ? _round(n / 100) : n;
  }
  return (params: out, errors: errors);
}

/// The catalog.
class TemplatesController extends ListController<AlertTemplate> {
  TemplatesController(this.client);

  final OpenlogClient client;

  /// Empty for every category.
  String category = '';

  /// Rule types this installation cannot use, with the server's reason.
  ///
  /// Asked for alongside the catalog because a template whose type is
  /// unavailable renders and previews and then fails on create, and the
  /// reason -- no SLO defined, no profiling data -- is the server's to give.
  List<AlertRuleTypeInfo> unavailable = const [];

  @override
  String get forbiddenKind => 'alertsForbidden';

  @override
  Future<List<AlertTemplate>> fetch() async {
    unavailable = [
      for (final t in (await client.alertRuleTypes()).types)
        if (!t.available) t,
    ];
    final list = (await client.alertTemplates(category: category)).templates;
    // Ratio templates need an instance to take a ratio of, and this screen
    // has none. Showing them would be offering a set-up that cannot finish.
    return [
      for (final t in list)
        if (templateUsable(t)) t,
    ];
  }
}

/// One template being set up: render, preview, create.
///
/// Three server calls in a row, each depending on the last, which is why this
/// is a controller rather than three futures in a widget: the screen has to
/// say which step it is on and what failed.
class TemplateSetupController extends ChangeNotifier {
  TemplateSetupController(this.client, this.template, {required this.language});

  final OpenlogClient client;
  final AlertTemplate template;
  final String language;

  /// The rule the server made of the values, kept exactly as it sent it.
  RenderedRule? rendered;

  /// What that rule would have done over the last six hours.
  AlertRulePreview? preview;

  /// The name of the rule once it exists, which is what the screen says
  /// instead of offering to create it a second time.
  String? created;

  bool busy = false;
  SessionFailure? failure;

  /// Renders and previews in one go: on a phone these are one action, and a
  /// rendered rule nobody previewed is not worth showing.
  Future<void> check(Map<String, Object?> params) async {
    busy = true;
    failure = null;
    rendered = null;
    preview = null;
    notifyListeners();
    try {
      final r = await client.renderAlertTemplate(
        template.id,
        params: params,
        language: language,
      );
      rendered = r;
      notifyListeners();
      preview = await client.previewAlertRule(r.rule);
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  /// Stores the rule the preview was of. Not a re-render: what was shown is
  /// what gets created.
  Future<void> create() async {
    final r = rendered;
    if (r == null) return;
    busy = true;
    failure = null;
    notifyListeners();
    try {
      await client.createAlertRule(r.rule);
      created = r.name;
    } on ApiUnreachable {
      failure = const SessionFailure('unreachable', '');
    } on ApiException catch (e) {
      failure = _failureOf(e);
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  SessionFailure _failureOf(ApiException e) => switch (e.status) {
    403 => const SessionFailure('alertsForbidden', ''),
    // 400 and 409 carry the server's own sentence -- "threshold out of
    // range", "a rule with that name exists" -- and nothing this screen
    // could write would be more useful.
    _ => SessionFailure('unexpected', e.message),
  };
}

/// How many times the rule would have opened an incident, over every series.
int previewIncidents(AlertRulePreview p) {
  var n = 0;
  for (final s in p.series) {
    n += s.incidents.length;
  }
  return n;
}

/// The series worth drawing: the one that fired most, or the first.
///
/// One line rather than all of them: a phone cannot show forty series, and
/// the one that caused the incidents is the one the number above refers to.
AlertPreviewSeries? busiestSeries(AlertRulePreview p) {
  if (p.series.isEmpty) return null;
  var best = p.series.first;
  for (final s in p.series) {
    if (s.incidents.length > best.incidents.length) best = s;
  }
  return best;
}
