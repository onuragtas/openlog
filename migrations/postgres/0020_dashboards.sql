-- openlog:phase expand
-- 0020_dashboards: custom dashboards of OQL widgets (docs/contracts/api.md "Dashboards", postgres.md, D-064).
-- A dashboard document (pages and widgets) is replaced as a whole on every save, guarded by dashboards.version.

CREATE TABLE IF NOT EXISTS dashboards (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name        text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    -- org: every member can read; private: only the creator (admins once the creator is deleted)
    visibility  text NOT NULL DEFAULT 'org' CHECK (visibility IN ('org', 'private')),
    -- [{"name","label","type","query","values","default","multi","include_all"}], validated by the API
    variables   jsonb NOT NULL DEFAULT '[]',
    version     integer NOT NULL DEFAULT 1,
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS dashboards_org_name ON dashboards (org_id, lower(name));

CREATE TABLE IF NOT EXISTS dashboard_pages (
    id           uuid PRIMARY KEY,
    dashboard_id uuid NOT NULL REFERENCES dashboards (id) ON DELETE CASCADE,
    position     integer NOT NULL CHECK (position >= 0),
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    UNIQUE (dashboard_id, position)
);

CREATE TABLE IF NOT EXISTS dashboard_widgets (
    id            uuid PRIMARY KEY,
    dashboard_id  uuid NOT NULL REFERENCES dashboards (id) ON DELETE CASCADE,
    page_id       uuid NOT NULL REFERENCES dashboard_pages (id) ON DELETE CASCADE,
    position      integer NOT NULL CHECK (position >= 0),
    title         text NOT NULL DEFAULT '' CHECK (length(title) <= 200),
    visualization text NOT NULL CHECK (visualization IN ('line', 'area', 'bar', 'table', 'billboard', 'pie', 'heatmap', 'markdown')),
    x             integer NOT NULL CHECK (x >= 0 AND x <= 11),
    y             integer NOT NULL CHECK (y >= 0 AND y <= 10000),
    w             integer NOT NULL CHECK (w BETWEEN 1 AND 12),
    h             integer NOT NULL CHECK (h BETWEEN 1 AND 50),
    query         text NOT NULL DEFAULT '' CHECK (length(query) <= 8192),
    markdown      text NOT NULL DEFAULT '' CHECK (length(markdown) <= 20000),
    unit          text NOT NULL DEFAULT '' CHECK (unit IN ('', 'number', 'percent', 'bytes', 'bytesPerSec', 'ms', 's')),
    -- [{"value": 100, "severity": "warning"|"critical"}]
    thresholds    jsonb NOT NULL DEFAULT '[]',
    -- {"stacked": bool, "legend": bool}
    options       jsonb NOT NULL DEFAULT '{}',
    CHECK (x + w <= 12),
    UNIQUE (page_id, position)
);

CREATE INDEX IF NOT EXISTS dashboard_widgets_dashboard ON dashboard_widgets (dashboard_id);
