package client

import "context"

// Service level objectives (docs/contracts/slo.md, api.md "Service level objectives"). An SLO is a definition
// only: the error budget and the burn rates are computed at query time, so the provider manages the six
// writable fields and nothing else.

// SLO is a service level objective as the API reads and writes it. ServiceNamespace and Environment follow
// apm.md §1: nil = every namespace/environment (aggregated), a string (also "") = an exact match — so they are
// pointers, not plain strings.
type SLO struct {
	ID               string  `json:"id,omitempty"`
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	ServiceName      string  `json:"service_name"`
	ServiceNamespace *string `json:"service_namespace"`
	Environment      *string `json:"environment"`
	SLIType          string  `json:"sli_type"`
	// LatencyThresholdMs applies to a latency SLI and is rejected for an availability one.
	LatencyThresholdMs *float64 `json:"latency_threshold_ms,omitempty"`
	Objective          float64  `json:"objective"`
	WindowDays         int      `json:"window_days"`

	CreatedByEmail string `json:"created_by_email,omitempty"`
	UpdatedByEmail string `json:"updated_by_email,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

// CreateSLO creates an SLO. The 201st SLO of an organization is refused with 409 failed_precondition.
func (c *Client) CreateSLO(ctx context.Context, in SLO) (*SLO, error) {
	in.ID, in.CreatedAt, in.UpdatedAt, in.CreatedByEmail, in.UpdatedByEmail = "", "", "", "", ""
	var out SLO
	if err := c.post(ctx, "/api/v1/slos", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetSLO reads one SLO definition (without its budget).
func (c *Client) GetSLO(ctx context.Context, id string) (*SLO, error) {
	var out SLO
	if err := c.get(ctx, "/api/v1/slos/"+urlPath(id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateSLO replaces an SLO.
func (c *Client) UpdateSLO(ctx context.Context, id string, in SLO) (*SLO, error) {
	in.ID, in.CreatedAt, in.UpdatedAt, in.CreatedByEmail, in.UpdatedByEmail = "", "", "", "", ""
	var out SLO
	if err := c.put(ctx, "/api/v1/slos/"+urlPath(id), in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteSLO removes an SLO. slo_burn rules that reference it are kept and start reporting evaluation errors
// (slo.md §3), so deleting an SLO that a rule still watches is a change to make deliberately.
func (c *Client) DeleteSLO(ctx context.Context, id string) error {
	return c.delete(ctx, "/api/v1/slos/"+urlPath(id))
}
