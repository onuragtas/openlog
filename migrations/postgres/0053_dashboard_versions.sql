-- openlog:phase expand
-- 0053_dashboard_versions: version history of custom dashboards (docs/contracts/api.md "Dashboards" › "Version
-- history", postgres.md, D-086). Every save stores the whole document as a snapshot; the newest 50 versions of a
-- dashboard are kept. Restoring a version saves it as a new version (restored_from).

CREATE TABLE IF NOT EXISTS dashboard_versions (
    dashboard_id  uuid NOT NULL REFERENCES dashboards (id) ON DELETE CASCADE,
    version       integer NOT NULL CHECK (version >= 1),
    -- {"name","description","visibility","variables":[…],"pages":[{"id","name","widgets":[widget with id]}]}
    document      jsonb NOT NULL,
    author_id     uuid REFERENCES users (id) ON DELETE SET NULL,
    restored_from integer CHECK (restored_from IS NULL OR restored_from >= 1),
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dashboard_id, version)
);

-- The current version of every existing dashboard becomes its first history entry.
INSERT INTO dashboard_versions (dashboard_id, version, document, author_id, created_at)
SELECT d.id, d.version,
       jsonb_build_object(
           'name', d.name, 'description', d.description, 'visibility', d.visibility, 'variables', d.variables,
           'pages', coalesce((
               SELECT jsonb_agg(jsonb_build_object(
                          'id', p.id::text, 'name', p.name,
                          'widgets', coalesce((
                              SELECT jsonb_agg(jsonb_build_object(
                                         'id', w.id::text, 'title', w.title, 'visualization', w.visualization,
                                         'layout', jsonb_build_object('x', w.x, 'y', w.y, 'w', w.w, 'h', w.h),
                                         'query', w.query, 'markdown', w.markdown, 'unit', w.unit,
                                         'thresholds', w.thresholds, 'options', w.options) ORDER BY w.position)
                              FROM dashboard_widgets w WHERE w.page_id = p.id), '[]'::jsonb)) ORDER BY p.position)
               FROM dashboard_pages p WHERE p.dashboard_id = d.id), '[]'::jsonb)),
       d.updated_by, d.updated_at
FROM dashboards d
ON CONFLICT DO NOTHING;
