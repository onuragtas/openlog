package alert

// Synthetic monitoring templates (D-132, D-140). A check writes its runs into `metrics` as gauges, which is
// the whole reason synthetics needed no rule type of its own; what it did need is the two rules everybody
// writes afterwards, so that "alert me when this check fails" is a button on the check rather than three
// metric names somebody has to know.
//
// Both rules are scoped by attributes: the runs carry check.id, check.name, check.type and location
// (synthetics/results.go), and no host or service, so they filter and group with attr.* alone.

import "github.com/onuragtas/openlog/internal/synthetics"

func pCheck() TemplateParam {
	return TemplateParam{Key: "check_id", Kind: ParamSyntheticCheck, Default: "",
		Label: txt("Check (empty = every check)", "Kontrol (boş = tüm kontroller)")}
}

// pCheckName carries the name into the rule's own name; the id reads as a UUID in an incident.
func pCheckName() TemplateParam {
	return TemplateParam{Key: "check_name", Kind: ParamText, Default: "",
		Label: txt("Check name (for the rule name)", "Kontrol adı (kural adı için)")}
}

// syntheticCondition builds a metric condition over one check or over every check.
//
// One incident per check and location either way: a check is pinned by its id, but a rule left open covers
// the checks added after it was written, which is the point of leaving it open.
func syntheticCondition(p *tplParams, s metricSpec, threshold float64) map[string]any {
	fs := []any{}
	if v := p.str("check_id"); v != "" {
		fs = append(fs, filter("attr.check.id", "eq", v))
	}
	fs = append(fs, s.extra...)
	c := map[string]any{"metric": s.metric, "aggregation": s.agg, "series_aggregation": s.seriesAgg,
		"window_seconds": int(p.num("window_seconds")), "filters": fs,
		"group_by": []string{"attr.check.name", "attr.location"}, "operator": s.operator, "threshold": threshold}
	if s.missing != "" {
		c["missing_data"] = s.missing
	}
	return c
}

func syntheticTemplates() []*Template {
	return []*Template{
		// A window with no run at all breaches too (missing_data breach): a check that stopped running is not
		// a check that is passing, and that is the failure mode a scheduler outage produces.
		{ID: "synthetic_check_failing", Category: TemplateSynthetics, RuleType: TypeMetricThreshold, Severity: SeverityCritical,
			Metric: synthetics.MetricSuccess,
			Name:   txt("Synthetic check failing", "Sentetik kontrol başarısız"),
			Description: txt("The share of successful runs in the window is below the threshold; a window without any run counts as failing.",
				"Penceredeki başarılı koşuların oranı eşiğin altında; hiç koşu olmayan pencere de başarısız sayılır."),
			Params: []TemplateParam{pCheck(), pCheckName(),
				pThreshold("ratio", 1, 0.01, 1, "Successful runs below", "Başarılı koşu oranı şunun altında"),
				pWindow(300, 60, 21600), pFor(0)},
			build: func(p *tplParams) (map[string]any, error) {
				return syntheticCondition(p, metricSpec{metric: synthetics.MetricSuccess, agg: "avg", seriesAgg: "min",
					operator: "lt", missing: "breach"}, p.num("threshold")), nil
			}},
		// Days left, not a date: the certificate of every https and tls check is read on the run it already
		// made (D-140), so one rule covers the expiry of everything that is checked at all.
		{ID: "synthetic_certificate_expiring", Category: TemplateSynthetics, RuleType: TypeMetricThreshold, Severity: SeverityWarning,
			Metric: synthetics.MetricCertExpiry,
			Name:   txt("Certificate expiring", "Sertifika bitmek üzere"),
			Description: txt("The certificate an https or tls check saw has fewer days left than the threshold (negative once expired).",
				"Bir https ya da tls kontrolünün gördüğü sertifikanın kalan günü eşikten az (süresi dolduysa negatif)."),
			Params: []TemplateParam{pCheck(), pCheckName(),
				pThreshold("count", 14, 1, 365, "Days left below", "Kalan gün şunun altında"),
				pWindow(3600, 600, 21600), pFor(0)},
			build: func(p *tplParams) (map[string]any, error) {
				// The soonest to expire decides, and the latest reading is the one that counts; a window
				// without a run says nothing about a certificate, so missing data is kept as it was.
				return syntheticCondition(p, metricSpec{metric: synthetics.MetricCertExpiry, agg: "last", seriesAgg: "min",
					operator: "lt"}, p.num("threshold")), nil
			}},
	}
}
