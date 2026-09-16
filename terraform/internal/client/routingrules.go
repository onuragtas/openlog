package client

import "context"

// Notification routing rules (docs/contracts/alerting.md §5.6): an ordered list per organization that decides
// which channels an incident reaches when it opens. The first enabled route whose match applies wins, then the
// route marked default. Routes have no version: the API replaces them as a whole.

// RouteMatcher matches one incident label.
type RouteMatcher struct {
	Label string `json:"label"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// RouteWindow restricts a route to local times (office hours). An end_time at or before start_time means the
// window ends the next day.
type RouteWindow struct {
	Timezone  string   `json:"timezone,omitempty"`
	Days      []string `json:"days,omitempty"`
	StartTime string   `json:"start_time"`
	EndTime   string   `json:"end_time"`
}

// RouteMatch is the condition of a route; its parts are AND-ed and an empty part matches everything.
type RouteMatch struct {
	Severities []string       `json:"severities,omitempty"`
	Services   []string       `json:"services,omitempty"`
	RuleTypes  []string       `json:"rule_types,omitempty"`
	Labels     []RouteMatcher `json:"labels,omitempty"`
	TimeWindow *RouteWindow   `json:"time_window,omitempty"`
}

// RoutingRule is a routing rule as the API reads and writes it.
type RoutingRule struct {
	ID         string     `json:"id,omitempty"`
	Name       string     `json:"name"`
	Position   int        `json:"position"`
	Enabled    bool       `json:"enabled"`
	IsDefault  bool       `json:"is_default"`
	Match      RouteMatch `json:"match"`
	ChannelIDs []string   `json:"channel_ids"`

	CreatedByEmail string `json:"created_by_email,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

// CreateRoutingRule creates a routing rule. A second default route, or the 100th rule, is refused with
// 409 failed_precondition.
func (c *Client) CreateRoutingRule(ctx context.Context, in RoutingRule) (*RoutingRule, error) {
	in.ID, in.CreatedAt, in.UpdatedAt, in.CreatedByEmail = "", "", "", ""
	var out RoutingRule
	if err := c.post(ctx, "/api/v1/alerts/routing-rules", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRoutingRule reads one routing rule.
func (c *Client) GetRoutingRule(ctx context.Context, id string) (*RoutingRule, error) {
	var out RoutingRule
	if err := c.get(ctx, "/api/v1/alerts/routing-rules/"+urlPath(id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateRoutingRule replaces a routing rule.
func (c *Client) UpdateRoutingRule(ctx context.Context, id string, in RoutingRule) (*RoutingRule, error) {
	in.ID, in.CreatedAt, in.UpdatedAt, in.CreatedByEmail = "", "", "", ""
	var out RoutingRule
	if err := c.put(ctx, "/api/v1/alerts/routing-rules/"+urlPath(id), in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteRoutingRule removes a routing rule. Open incidents keep the channels they were routed to.
func (c *Client) DeleteRoutingRule(ctx context.Context, id string) error {
	return c.delete(ctx, "/api/v1/alerts/routing-rules/"+urlPath(id))
}
