package client

import "context"

// Dashboards (docs/contracts/api.md "Dashboards"). A dashboard is one document: PUT replaces it whole and
// carries the version that was read, so a concurrent change is refused with 409 failed_precondition rather
// than silently overwritten. Page and widget ids are kept when the write carries ids of the same dashboard,
// which is why the provider sends the ids it last read back.

// Layout is a widget position on the 12-column grid.
type Layout struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Threshold colors a widget value.
type Threshold struct {
	Value    float64 `json:"value"`
	Severity string  `json:"severity"`
}

// WidgetOptions are visualization options. Legend is a pointer because the API distinguishes "unset" from
// "off".
type WidgetOptions struct {
	Stacked bool  `json:"stacked,omitempty"`
	Legend  *bool `json:"legend,omitempty"`
}

// Widget is one dashboard widget. Markdown widgets carry Markdown instead of Query.
type Widget struct {
	ID            string        `json:"id,omitempty"`
	Title         string        `json:"title"`
	Visualization string        `json:"visualization"`
	Layout        Layout        `json:"layout"`
	Query         string        `json:"query"`
	Markdown      string        `json:"markdown"`
	Unit          string        `json:"unit"`
	Thresholds    []Threshold   `json:"thresholds"`
	Options       WidgetOptions `json:"options"`
}

// Page is a dashboard page.
type Page struct {
	ID      string   `json:"id,omitempty"`
	Name    string   `json:"name"`
	Widgets []Widget `json:"widgets"`
}

// Variable is a dashboard template variable ({{name}} in widget queries).
type Variable struct {
	Name       string   `json:"name"`
	Label      string   `json:"label"`
	Type       string   `json:"type"`
	Query      string   `json:"query"`
	Values     []string `json:"values"`
	Default    []string `json:"default"`
	Multi      bool     `json:"multi"`
	IncludeAll bool     `json:"include_all"`
}

// Dashboard is a dashboard document as the API reads and writes it.
type Dashboard struct {
	ID          string     `json:"id,omitempty"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Visibility  string     `json:"visibility,omitempty"`
	Variables   []Variable `json:"variables"`
	Pages       []Page     `json:"pages"`
	// Version is read-only in the response and required in a PUT: the version that was read.
	Version        int    `json:"version,omitempty"`
	CreatedByEmail string `json:"created_by_email,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

// DashboardSummary is a list entry (GET /api/v1/dashboards).
type DashboardSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"`
	PageCount   int    `json:"page_count"`
	WidgetCount int    `json:"widget_count"`
}

// CreateDashboard creates a dashboard. The response carries the page and widget ids the API assigned.
func (c *Client) CreateDashboard(ctx context.Context, in Dashboard) (*Dashboard, error) {
	in.ID, in.Version, in.CreatedAt, in.UpdatedAt, in.CreatedByEmail = "", 0, "", "", ""
	var out Dashboard
	if err := c.post(ctx, "/api/v1/dashboards", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetDashboard reads one dashboard document.
func (c *Client) GetDashboard(ctx context.Context, id string) (*Dashboard, error) {
	var out Dashboard
	if err := c.get(ctx, "/api/v1/dashboards/"+urlPath(id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListDashboards reads the dashboard list (data source lookup by name); entries are summaries, not documents.
func (c *Client) ListDashboards(ctx context.Context) ([]DashboardSummary, error) {
	var out struct {
		Dashboards []DashboardSummary `json:"dashboards"`
	}
	if err := c.get(ctx, "/api/v1/dashboards", &out); err != nil {
		return nil, err
	}
	return out.Dashboards, nil
}

// FindDashboardByName resolves a dashboard name to its document. Names are not unique in the API, so an
// ambiguous name is an error rather than an arbitrary pick.
func (c *Client) FindDashboardByName(ctx context.Context, name string) (*Dashboard, error) {
	list, err := c.ListDashboards(ctx)
	if err != nil {
		return nil, err
	}
	var found []DashboardSummary
	for _, d := range list {
		if d.Name == name {
			found = append(found, d)
		}
	}
	switch len(found) {
	case 0:
		return nil, notFoundByName("dashboard", name)
	case 1:
		return c.GetDashboard(ctx, found[0].ID)
	default:
		return nil, ambiguousName("dashboard", name, len(found))
	}
}

// FindChannelByName resolves a channel name to the channel.
func (c *Client) FindChannelByName(ctx context.Context, name string) (*Channel, error) {
	list, err := c.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	var found []Channel
	for _, ch := range list {
		if ch.Name == name {
			found = append(found, ch)
		}
	}
	switch len(found) {
	case 0:
		return nil, notFoundByName("notification channel", name)
	case 1:
		return &found[0], nil
	default:
		return nil, ambiguousName("notification channel", name, len(found))
	}
}

// UpdateDashboard replaces a dashboard. version is the version the provider last read.
func (c *Client) UpdateDashboard(ctx context.Context, id string, in Dashboard, version int) (*Dashboard, error) {
	in.ID, in.CreatedAt, in.UpdatedAt, in.CreatedByEmail = "", "", "", ""
	in.Version = version
	var out Dashboard
	if err := c.put(ctx, "/api/v1/dashboards/"+urlPath(id), in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteDashboard removes a dashboard, its versions, share links and scheduled reports.
func (c *Client) DeleteDashboard(ctx context.Context, id string) error {
	return c.delete(ctx, "/api/v1/dashboards/"+urlPath(id))
}
