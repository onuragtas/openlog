package config

// Cost configures infrastructure cost monitoring (docs/contracts/cost.md, D-134): the api prices
// every host from the instance facts its agent reported and splits that price over the services
// and containers on it. The estimate uses a static price table compiled into the binary; the
// override file lets an operator correct a price without waiting for a release.
type Cost struct {
	// Enabled serves /api/v1/costs/* (OPENLOG_COST_ENABLED).
	Enabled bool
	// PricesFile is an optional JSON file merged over the built-in table
	// (OPENLOG_COST_PRICES_FILE). An unreadable or invalid file stops the api rather than
	// pricing silently with numbers the operator meant to replace.
	PricesFile string
}

func loadCost(p *parser) Cost {
	return Cost{
		Enabled:    p.bool("OPENLOG_COST_ENABLED", true),
		PricesFile: p.str("OPENLOG_COST_PRICES_FILE", ""),
	}
}
