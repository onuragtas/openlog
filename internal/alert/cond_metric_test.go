package alert

import (
	"encoding/json"
	"testing"
)

// Chart alert shortcuts of the IIS panel prefill the selected site (resource.iis.site) next to the instance filters.
func TestMetricConditionIISResourceFilters(t *testing.T) {
	raw := `{"metric":"iis.request.count","aggregation":"rate","operator":"gt","threshold":100,
		"filters":[{"field":"resource.openlog.discovery.id","op":"eq","values":["iis"]},
			{"field":"resource.openlog.discovery.instance","op":"eq","values":["w3svc"]},
			{"field":"resource.iis.site","op":"eq","values":["Default Web Site"]},
			{"field":"resource.iis.application_pool","op":"eq","values":["DefaultAppPool"]}],
		"group_by":["host","resource.iis.site"]}`
	c, err := ruleTypes[TypeMetricThreshold].Parse(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m := c.(MetricCondition)
	if len(m.Filters) != 4 || m.Filters[2].Field != "resource.iis.site" || m.Filters[2].Values[0] != "Default Web Site" {
		t.Fatalf("filters = %+v", m.Filters)
	}
	if _, err := ruleTypes[TypeMetricThreshold].Parse(json.RawMessage(`{"metric":"m","operator":"gt","threshold":1,"filters":[{"field":"resource.bad key","op":"eq","values":["x"]}]}`)); err == nil {
		t.Fatal("invalid resource key accepted")
	}
}
